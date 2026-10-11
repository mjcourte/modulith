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

	"gyoza/cmd/internal/wiring"
	ledgerpb "gyoza/contracts/gen/go/gyoza/storageledger/v1"
	managerpb "gyoza/contracts/gen/go/gyoza/storagemanager/v1"
	"gyoza/platform/config"
	"gyoza/platform/postgres"
	"gyoza/platform/shutdown"
	"gyoza/services/storageledger"
	"gyoza/services/storagemanager"
)

func main() {
	if err := run(); err != nil {
		slog.Error("gyoza failed", "err", err)
		os.Exit(1)
	}
}

// run holds the real main so that deferred calls run before the process
// exits; os.Exit skips defers.
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The monolith's settings are the union of every service's settings.
	env := config.FromEnv()
	dbURL := env.Required("GYOZA_DATABASE_URL")
	logLevel := slog.LevelInfo
	env.Text("GYOZA_LOG_LEVEL", &logLevel)
	shutdownTimeout := env.Duration("GYOZA_SHUTDOWN_TIMEOUT", 20*time.Second)
	grpcAddr := env.String("GYOZA_GRPC_ADDR", "localhost:9090")
	storage := wiring.StorageManagerFromEnv(env)
	if err := env.Err(); err != nil {
		return err
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})))

	// One pool shared by every service; each still only touches its own schema.
	pool, err := postgres.Connect(ctx, dbURL, "gyoza")
	if err != nil {
		return err
	}
	if err := storageledger.Migrate(ctx, pool); err != nil {
		pool.Close() // nothing is running yet, so this returns promptly
		return err
	}
	// In-process: the manager calls the ledger directly, no gRPC in between.
	ledger := storageledger.NewPostgres(pool)
	manager, err := wiring.NewStorageManager(ctx, pool, ledger, storage)
	if err != nil {
		pool.Close()
		return err
	}

	// Until the gateway exists, the services are reachable over gRPC so
	// they can be called by hand (go tool buf curl, see contracts/README.md).
	lis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		pool.Close()
		return err
	}
	server := grpc.NewServer()
	ledgerpb.RegisterLedgerServiceServer(server, storageledger.NewGRPCServer(ledger))
	managerpb.RegisterStorageManagerServiceServer(server, storagemanager.NewGRPCServer(manager))
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(lis) }()

	cleanupCtx, stopCleanup := context.WithCancel(context.WithoutCancel(ctx))
	defer stopCleanup()
	cleanupDone := wiring.StartCleanup(cleanupCtx, manager, storage.CleanupInterval)
	slog.Info("gyoza started", "grpc", lis.Addr().String(), "bucket", storage.Manager.Bucket)

	select {
	case <-ctx.Done():
	case err := <-serveErr:
		stopCleanup()
		<-cleanupDone
		pool.Close()
		return fmt.Errorf("grpc server: %w", err)
	}
	stop() // from here a second Ctrl+C kills the process instead of waiting
	slog.Info("shutting down", "timeout", shutdownTimeout)

	// ctx is cancelled now, so shutdown work gets its own deadline.
	// WithoutCancel keeps ctx's values but drops its cancellation.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()

	// Stop in reverse order of startup: the server, the cleanup job, then
	// the pool, because both of them still need it until they've stopped.
	if err := shutdown.Within(shutdownCtx, server.GracefulStop); err != nil {
		server.Stop()
		slog.Warn("grpc calls cut off at shutdown deadline")
	}
	stopCleanup()
	if err := shutdown.Within(shutdownCtx, func() { <-cleanupDone }); err != nil {
		slog.Warn("cleanup run cut off at shutdown deadline")
	}
	if err := shutdown.Within(shutdownCtx, pool.Close); err != nil {
		return err
	}
	slog.Info("stopped cleanly")
	return nil
}
