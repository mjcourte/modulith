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
	"github.com/mjcourte/modulith/services/documents"
	"github.com/mjcourte/modulith/services/gateway"
	"github.com/mjcourte/modulith/services/iam"
	"github.com/mjcourte/modulith/services/storage"
)

func main() {
	if err := run(); err != nil {
		slog.Error("iam failed", "err", err)
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
	// deployments set ":9090". Differs from the other binaries' defaults so
	// they can all run on one machine.
	grpcAddr := env.String("MODULITH_GRPC_ADDR", "localhost:9093")
	// cmd/storage's local default. The deletion cascades call it; while it's
	// unreachable they stop part-way and the cleanup loop finishes them later.
	storageAddr := env.String("MODULITH_STORAGE_ADDR", "localhost:9091")
	// cmd/documents' local default. Clones call it; while it's unreachable they
	// stay hidden and the cleanup loop finishes them later.
	documentsAddr := env.String("MODULITH_DOCUMENTS_ADDR", "localhost:9094")
	settings := iam.SettingsFromEnv(env)
	// Deleting an account also deletes its sign-in account (SuperTokens core).
	idp := gateway.IdentityProviderFromEnv(env)
	if err := env.Err(); err != nil {
		return err
	}
	tel, err := telemetry.Setup(ctx, "iam", telemetrySettings)
	if err != nil {
		return err
	}
	slog.SetDefault(slog.New(tel.LogHandler(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel}), logLevel)))
	stopDebug, err := telemetry.StartDebug(telemetrySettings.DebugAddr)
	if err != nil {
		return fmt.Errorf("debug listener: %w", err)
	}

	// Plaintext: services only talk inside the platform's private network.
	// NewClient doesn't connect yet, so storage may start after us (it needs
	// us too, to authorize).
	storageConn, err := grpc.NewClient(storageAddr, grpc.WithTransportCredentials(insecure.NewCredentials()), telemetry.DialOption())
	if err != nil {
		return fmt.Errorf("storage client: %w", err)
	}
	// Closed last (defers run after the shutdown below), once nothing uses it.
	defer storageConn.Close()
	documentsConn, err := grpc.NewClient(documentsAddr, grpc.WithTransportCredentials(insecure.NewCredentials()), telemetry.DialOption())
	if err != nil {
		return fmt.Errorf("documents client: %w", err)
	}
	defer documentsConn.Close()

	pool, err := postgres.Connect(ctx, dbURL, "iam")
	if err != nil {
		return err
	}
	store, err := iam.OpenStore(ctx, pool)
	if err != nil {
		pool.Close() // nothing is running yet, so this returns promptly
		return err
	}
	manager, err := iam.NewManager(store, storage.NewGRPCClient(storageConn), documents.NewGRPCClient(documentsConn), idp, settings.Manager)
	if err != nil {
		pool.Close()
		return err
	}

	lis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		pool.Close()
		return err
	}
	server := grpc.NewServer(telemetry.ServerOption())
	iam.RegisterGRPC(server, manager, iam.NewStorageAuthorizer(store))
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(lis) }()

	// The cleanup loop gets its own context, cancelled at shutdown before the
	// pool closes, since a run in progress still needs the database.
	jobsCtx, stopJobs := context.WithCancel(context.WithoutCancel(ctx))
	defer stopJobs()
	cleanupDone := iam.StartCleanup(jobsCtx, manager, settings.CleanupInterval)
	slog.Info("iam started", "grpc", lis.Addr().String(), "storage", storageAddr)

	select {
	case <-ctx.Done():
	case err := <-serveErr:
		stopJobs()
		<-cleanupDone
		pool.Close()
		return fmt.Errorf("grpc server: %w", err)
	}
	stop() // from here a second Ctrl+C kills the process instead of waiting
	slog.Info("shutting down", "timeout", shutdownTimeout)
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()

	// Reverse order of startup: stop taking calls, stop the cleanup loop,
	// then close the pool they used.
	if err := shutdown.Within(shutdownCtx, server.GracefulStop); err != nil {
		server.Stop()
		slog.Warn("grpc calls cut off at shutdown deadline")
	}
	stopJobs()
	if err := shutdown.Within(shutdownCtx, func() { <-cleanupDone }); err != nil {
		slog.Warn("cleanup run cut off at shutdown deadline")
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
