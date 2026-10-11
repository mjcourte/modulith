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

	pb "gyoza/contracts/gen/go/gyoza/storageledger/v1"
	"gyoza/platform/config"
	"gyoza/platform/postgres"
	"gyoza/platform/shutdown"
	storageledger "gyoza/services/storageledger"
)

func main() {
	if err := run(); err != nil {
		slog.Error("storage-ledger failed", "err", err)
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
	// localhost by default so local runs don't listen on the network; deployments set ":9090".
	grpcAddr := env.String("GYOZA_GRPC_ADDR", "localhost:9090")
	if err := env.Err(); err != nil {
		return err
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})))

	pool, err := postgres.Connect(ctx, dbURL, "storage-ledger")
	if err != nil {
		return err
	}
	if err := storageledger.Migrate(ctx, pool); err != nil {
		pool.Close() // nothing is running yet, so this returns promptly
		return err
	}
	svc := storageledger.NewPostgres(pool)

	lis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		pool.Close()
		return err
	}
	server := grpc.NewServer()
	pb.RegisterLedgerServiceServer(server, storageledger.NewGRPCServer(svc))
	// Serve blocks until the server stops, so it runs in its own goroutine.
	// The buffered channel lets it report a failure without waiting for a reader.
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(lis) }()
	slog.Info("storage-ledger started", "grpc", lis.Addr().String())

	// Run until a signal arrives or the server fails on its own.
	select {
	case <-ctx.Done():
	case err := <-serveErr:
		pool.Close()
		return fmt.Errorf("grpc server: %w", err)
	}
	stop() // from here a second Ctrl+C kills the process instead of waiting
	slog.Info("shutting down", "timeout", shutdownTimeout)

	// ctx is cancelled now, so shutdown work gets its own deadline.
	// WithoutCancel keeps ctx's values but drops its cancellation.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()

	// Stop in reverse order of startup. GracefulStop refuses new calls and
	// waits for in-flight ones; if they outlast the deadline, Stop cuts them off.
	if err := shutdown.Within(shutdownCtx, server.GracefulStop); err != nil {
		server.Stop()
		slog.Warn("grpc calls cut off at shutdown deadline")
	}
	// The pool goes last because in-flight calls still needed it.
	if err := shutdown.Within(shutdownCtx, pool.Close); err != nil {
		return err
	}
	slog.Info("stopped cleanly")
	return nil
}
