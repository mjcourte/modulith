package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/mjcourte/modulith/platform/config"
	"github.com/mjcourte/modulith/platform/postgres"
	"github.com/mjcourte/modulith/platform/shutdown"
	"github.com/mjcourte/modulith/platform/telemetry"
	"github.com/mjcourte/modulith/services/iam"
	"github.com/mjcourte/modulith/services/mediaprocessor"
	"github.com/mjcourte/modulith/services/storage"
)

func main() {
	if err := run(); err != nil {
		slog.Error("storage failed", "err", err)
		os.Exit(1)
	}
}

// run holds the real main so that deferred calls run before the process
// exits; os.Exit skips defers.
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	env := config.FromEnv()
	dbURL := env.Required("MODULITH_DATABASE_URL")
	logLevel := slog.LevelInfo
	env.Text("MODULITH_LOG_LEVEL", &logLevel)
	telemetrySettings := telemetry.SettingsFromEnv(env)
	shutdownTimeout := env.Duration("MODULITH_SHUTDOWN_TIMEOUT", 20*time.Second)
	// localhost by default so local runs don't listen on the network;
	// deployments set ":9090". The default differs from the monolith's so both
	// can run on one machine.
	grpcAddr := env.String("MODULITH_GRPC_ADDR", "localhost:9091")
	// cmd/mediaprocessor's local default. While it's unreachable, video and
	// audio uploads wait in the queue without using up their attempts.
	mediaAddr := env.String("MODULITH_MEDIAPROCESSOR_ADDR", "localhost:9092")
	// cmd/iam's local default. It answers every permission check, so while
	// it's unreachable user calls fail (never allowed by default).
	iamAddr := env.String("MODULITH_IAM_ADDR", "localhost:9093")
	settings := storage.SettingsFromEnv(env)
	if err := env.Err(); err != nil {
		return err
	}
	tel, err := telemetry.Setup(ctx, "storage", telemetrySettings)
	if err != nil {
		return err
	}
	slog.SetDefault(slog.New(tel.LogHandler(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel}), logLevel)))
	stopDebug, err := telemetry.StartDebug(telemetrySettings.DebugAddr)
	if err != nil {
		return fmt.Errorf("debug listener: %w", err)
	}

	// Plaintext: services only talk inside the platform's private network.
	// NewClient doesn't connect yet, so mediaprocessor may start after us.
	mediaConn, err := grpc.NewClient(mediaAddr, grpc.WithTransportCredentials(insecure.NewCredentials()), telemetry.DialOption())
	if err != nil {
		return fmt.Errorf("mediaprocessor client: %w", err)
	}
	// Closed last (defers run after the shutdown below), once the probe job
	// using it has stopped. Close doesn't wait on the network.
	defer mediaConn.Close()
	iamConn, err := grpc.NewClient(iamAddr, grpc.WithTransportCredentials(insecure.NewCredentials()), telemetry.DialOption())
	if err != nil {
		return fmt.Errorf("iam client: %w", err)
	}
	defer iamConn.Close()

	pool, err := postgres.Connect(ctx, dbURL, "storage")
	if err != nil {
		return err
	}
	// The manager calls the ledger in-process; other services (the documents service's
	// snapshots, iam's cascades) call it over gRPC.
	manager, err := storage.Open(ctx, pool, settings, iam.NewAuthzClient(iamConn))
	if err != nil {
		pool.Close() // nothing is running yet, so this returns promptly
		return err
	}

	lis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		pool.Close()
		return err
	}
	server := grpc.NewServer(telemetry.ServerOption())
	storage.RegisterGRPC(server, manager)
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(lis) }()

	// Background jobs share a context, cancelled at shutdown before the pool
	// closes, since a run in progress still needs the database.
	jobsCtx, stopJobs := context.WithCancel(context.WithoutCancel(ctx))
	defer stopJobs()
	cleanupDone := storage.StartCleanup(jobsCtx, manager, settings.CleanupInterval)
	probeDone := storage.StartMediaProbe(jobsCtx, manager, mediaprocessor.NewGRPCClient(mediaConn), settings.ProbeInterval)
	jobsDone := func() { <-cleanupDone; <-probeDone }
	slog.Info("storage started", "grpc", lis.Addr().String(), "bucket", settings.Manager.Bucket, "mediaprocessor", mediaAddr, "iam", iamAddr)

	// Run until a signal arrives or the server fails on its own.
	select {
	case <-ctx.Done():
	case err := <-serveErr:
		stopJobs()
		jobsDone()
		pool.Close()
		return fmt.Errorf("grpc server: %w", err)
	}
	stop() // from here a second Ctrl+C kills the process instead of waiting
	slog.Info("shutting down", "timeout", shutdownTimeout)
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()

	// Reverse order of startup: stop taking calls, stop the background jobs,
	// then close the pool they used.
	if err := shutdown.Within(shutdownCtx, server.GracefulStop); err != nil {
		server.Stop()
		slog.Warn("grpc calls cut off at shutdown deadline")
	}
	stopJobs()
	if err := shutdown.Within(shutdownCtx, jobsDone); err != nil {
		slog.Warn("background jobs cut off at shutdown deadline")
	}
	if err := shutdown.Within(shutdownCtx, pool.Close); err != nil {
		return err
	}
	if err := stopDebug(shutdownCtx); err != nil {
		slog.Warn("debug listener shutdown", "err", err)
	}
	slog.Info("stopped cleanly")
	// Last: flush the spans and logs of everything above.
	if err := tel.Shutdown(shutdownCtx); err != nil {
		slog.Warn("telemetry flush failed", "err", err)
	}
	return nil
}
