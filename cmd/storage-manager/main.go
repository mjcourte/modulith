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

	"gyoza/cmd/internal/wiring"
	pb "gyoza/contracts/gen/go/gyoza/storagemanager/v1"
	"gyoza/platform/config"
	"gyoza/platform/postgres"
	"gyoza/platform/shutdown"
	"gyoza/services/storageledger"
	"gyoza/services/storagemanager"
)

func main() {
	if err := run(); err != nil {
		slog.Error("storage-manager failed", "err", err)
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
	// deployments set ":9090". The default differs from the ledger's so both
	// can run on one machine.
	grpcAddr := env.String("GYOZA_GRPC_ADDR", "localhost:9091")
	ledgerAddr := env.Required("GYOZA_LEDGER_ADDR") // e.g. localhost:9090
	settings := wiring.StorageManagerFromEnv(env)
	if err := env.Err(); err != nil {
		return err
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})))

	// The ledger is another service here: reach it over gRPC. NewClient
	// doesn't connect yet; the first call does, and reconnects as needed.
	// Plain-text for now: service-to-service TLS comes with deployment.
	ledgerConn, err := grpc.NewClient(ledgerAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("ledger client: %w", err)
	}
	defer ledgerConn.Close() // returns at once: it only drops connections
	ledger := storageledger.NewGRPCClient(ledgerConn)

	pool, err := postgres.Connect(ctx, dbURL, "storage-manager")
	if err != nil {
		return err
	}
	manager, err := wiring.NewStorageManager(ctx, pool, ledger, settings)
	if err != nil {
		pool.Close() // nothing is running yet, so this returns promptly
		return err
	}

	lis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		pool.Close()
		return err
	}
	server := grpc.NewServer()
	pb.RegisterStorageManagerServiceServer(server, storagemanager.NewGRPCServer(manager))
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(lis) }()

	// The cleanup job gets its own context, cancelled at shutdown before the
	// pool closes, since a run in progress still needs the database.
	cleanupCtx, stopCleanup := context.WithCancel(context.WithoutCancel(ctx))
	defer stopCleanup()
	cleanupDone := wiring.StartCleanup(cleanupCtx, manager, settings.CleanupInterval)
	slog.Info("storage-manager started", "grpc", lis.Addr().String(), "ledger", ledgerAddr, "bucket", settings.Manager.Bucket)

	// Run until a signal arrives or the server fails on its own.
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
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()

	// Reverse order of startup: stop taking calls, stop the cleanup job,
	// then close the pool both of them used.
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
