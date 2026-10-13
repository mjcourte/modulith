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

	"gyoza/platform/config"
	"gyoza/platform/postgres"
	"gyoza/platform/shutdown"
	"gyoza/services/iam"
	"gyoza/services/storage"
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
	dbURL := env.Required("GYOZA_DATABASE_URL")
	logLevel := slog.LevelInfo
	env.Text("GYOZA_LOG_LEVEL", &logLevel)
	shutdownTimeout := env.Duration("GYOZA_SHUTDOWN_TIMEOUT", 20*time.Second)
	// localhost by default so local runs don't listen on the network;
	// deployments set ":9090". Differs from the other binaries' defaults so
	// they can all run on one machine.
	grpcAddr := env.String("GYOZA_GRPC_ADDR", "localhost:9093")
	// cmd/storage's local default. The deletion cascades call it; while it's
	// unreachable they stop part-way and the cleanup loop finishes them later.
	storageAddr := env.String("GYOZA_STORAGE_ADDR", "localhost:9091")
	settings := iam.SettingsFromEnv(env)
	if err := env.Err(); err != nil {
		return err
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})))

	// Plaintext: services only talk inside the platform's private network.
	// NewClient doesn't connect yet, so storage may start after us (it needs
	// us too, to authorize).
	storageConn, err := grpc.NewClient(storageAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("storage client: %w", err)
	}
	// Closed last (defers run after the shutdown below), once nothing uses it.
	defer storageConn.Close()

	pool, err := postgres.Connect(ctx, dbURL, "iam")
	if err != nil {
		return err
	}
	store, err := iam.OpenStore(ctx, pool)
	if err != nil {
		pool.Close() // nothing is running yet, so this returns promptly
		return err
	}
	// No identity provider until the gateway (and SuperTokens) exists.
	manager, err := iam.NewManager(store, storage.NewGRPCClient(storageConn), iam.NoIdentityProvider{}, settings.Manager)
	if err != nil {
		pool.Close()
		return err
	}

	lis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		pool.Close()
		return err
	}
	server := grpc.NewServer()
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
	slog.Info("stopped cleanly")
	return nil
}
