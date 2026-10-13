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
	gnats "gyoza/platform/nats"
	"gyoza/platform/shutdown"
	"gyoza/services/documents"
	"gyoza/services/iam"
	"gyoza/services/storage"
)

func main() {
	if err := run(); err != nil {
		slog.Error("documents failed", "err", err)
		os.Exit(1)
	}
}

// run holds the real main so that deferred calls run before the process
// exits; os.Exit skips defers.
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	env := config.FromEnv()
	logLevel := slog.LevelInfo
	env.Text("GYOZA_LOG_LEVEL", &logLevel)
	shutdownTimeout := env.Duration("GYOZA_SHUTDOWN_TIMEOUT", 20*time.Second)
	// localhost by default so local runs don't listen on the network;
	// deployments set ":9090". Differs from the other binaries' defaults so
	// all can run on one machine.
	grpcAddr := env.String("GYOZA_GRPC_ADDR", "localhost:9094")
	// cmd/storage's and cmd/iam's local defaults. Storage keeps the snapshots
	// and asset uses; iam answers every permission check, so while it's
	// unreachable user calls fail (never allowed by default).
	storageAddr := env.String("GYOZA_STORAGE_ADDR", "localhost:9091")
	iamAddr := env.String("GYOZA_IAM_ADDR", "localhost:9093")
	settings := documents.SettingsFromEnv(env)
	if err := env.Err(); err != nil {
		return err
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})))

	// Plaintext inside the platform's private network. NewClient doesn't
	// connect yet, so storage and iam may start after us.
	storageConn, err := grpc.NewClient(storageAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("storage client: %w", err)
	}
	defer storageConn.Close() // after the shutdown below: Close saves through storage
	iamConn, err := grpc.NewClient(iamAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("iam client: %w", err)
	}
	defer iamConn.Close()

	nc, err := gnats.Connect(ctx, settings.NATSURL, "documents")
	if err != nil {
		return err
	}
	docs := documents.Open(settings, storage.NewGRPCClient(storageConn), iam.NewAuthzClient(iamConn), nc)

	lis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		nc.Close(context.WithoutCancel(ctx))
		return err
	}
	server := grpc.NewServer()
	documents.RegisterGRPC(server, docs)
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(lis) }()

	jobsCtx, stopJobs := context.WithCancel(context.WithoutCancel(ctx))
	defer stopJobs()
	consumerDone := documents.StartConsumer(jobsCtx, docs, nc, settings.Consumer)
	slog.Info("documents started", "grpc", lis.Addr().String(), "replica", settings.Manager.Replica, "storage", storageAddr, "iam", iamAddr)

	var failed error
	select {
	case <-ctx.Done():
	case err := <-serveErr:
		failed = fmt.Errorf("grpc server: %w", err)
	}
	stop() // from here a second Ctrl+C kills the process instead of waiting
	slog.Info("shutting down", "timeout", shutdownTimeout)
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()

	// Reverse order of startup: stop taking calls, stop the consumer, save
	// every open document (through storage, announcing on NATS), then NATS.
	if err := shutdown.Within(shutdownCtx, server.GracefulStop); err != nil {
		server.Stop()
		slog.Warn("grpc calls cut off at shutdown deadline")
	}
	stopJobs()
	if err := shutdown.Within(shutdownCtx, func() { <-consumerDone }); err != nil {
		slog.Warn("consumer cut off at shutdown deadline")
	}
	// Unsaved documents aren't lost if this fails: their deltas stay in the
	// stream until a snapshot covering them is stored.
	if err := docs.Close(shutdownCtx); err != nil {
		slog.Error("saving open documents", "err", err)
	}
	if err := nc.Close(shutdownCtx); err != nil {
		slog.Warn("closing NATS", "err", err)
	}
	if failed != nil {
		return failed
	}
	slog.Info("stopped cleanly")
	return nil
}
