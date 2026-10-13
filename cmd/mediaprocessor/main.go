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

	"gyoza/platform/config"
	"gyoza/platform/shutdown"
	"gyoza/services/mediaprocessor"
)

func main() {
	if err := run(); err != nil {
		slog.Error("mediaprocessor failed", "err", err)
		os.Exit(1)
	}
}

// run holds the real main so that deferred calls run before the process
// exits; os.Exit skips defers.
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// No database or S3 settings: this binary runs ffprobe on untrusted
	// files, so it is given no secrets at all (see mediaprocessor/PLAN.md).
	env := config.FromEnv()
	logLevel := slog.LevelInfo
	env.Text("GYOZA_LOG_LEVEL", &logLevel)
	shutdownTimeout := env.Duration("GYOZA_SHUTDOWN_TIMEOUT", 20*time.Second)
	// localhost by default so local runs don't listen on the network;
	// deployments set ":9090". Differs from the other binaries' defaults so
	// they can all run on one machine.
	grpcAddr := env.String("GYOZA_GRPC_ADDR", "localhost:9092")
	if err := env.Err(); err != nil {
		return err
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})))

	// Probing is this binary's only job, so a missing ffprobe is fatal here
	// (the monolith only warns).
	prober, err := mediaprocessor.NewFFprobe()
	if err != nil {
		return err
	}

	lis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		return err
	}
	server := grpc.NewServer()
	mediaprocessor.RegisterGRPC(server, prober)
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(lis) }()
	slog.Info("mediaprocessor started", "grpc", lis.Addr().String())

	select {
	case <-ctx.Done():
	case err := <-serveErr:
		return fmt.Errorf("grpc server: %w", err)
	}
	stop() // from here a second Ctrl+C kills the process instead of waiting
	slog.Info("shutting down", "timeout", shutdownTimeout)
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()

	// Probes in progress finish (each is bounded by its own timeout).
	if err := shutdown.Within(shutdownCtx, server.GracefulStop); err != nil {
		server.Stop()
		slog.Warn("grpc calls cut off at shutdown deadline")
	}
	slog.Info("stopped cleanly")
	return nil
}
