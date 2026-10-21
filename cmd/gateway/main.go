package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/mjcourte/modulith/platform/config"
	gnats "github.com/mjcourte/modulith/platform/nats"
	"github.com/mjcourte/modulith/platform/shutdown"
	"github.com/mjcourte/modulith/platform/telemetry"
	"github.com/mjcourte/modulith/services/documents"
	"github.com/mjcourte/modulith/services/gateway"
	"github.com/mjcourte/modulith/services/iam"
	"github.com/mjcourte/modulith/services/storage"
)

func main() {
	if err := run(); err != nil {
		slog.Error("gateway failed", "err", err)
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
	env.Text("MODULITH_LOG_LEVEL", &logLevel)
	telemetrySettings := telemetry.SettingsFromEnv(env)
	shutdownTimeout := env.Duration("MODULITH_SHUTDOWN_TIMEOUT", 20*time.Second)
	// cmd/iam's local default. Sign-ins fail while it's unreachable.
	iamAddr := env.String("MODULITH_IAM_ADDR", "localhost:9093")
	// cmd/storage's and cmd/documents' local defaults. Their routes fail
	// while they're unreachable.
	storageAddr := env.String("MODULITH_STORAGE_ADDR", "localhost:9091")
	documentsAddr := env.String("MODULITH_DOCUMENTS_ADDR", "localhost:9094")
	settings := gateway.SettingsFromEnv(env)
	if err := env.Err(); err != nil {
		return err
	}
	tel, err := telemetry.Setup(ctx, "gateway", telemetrySettings)
	if err != nil {
		return err
	}
	slog.SetDefault(slog.New(tel.LogHandler(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel}), logLevel)))
	stopDebug, err := telemetry.StartDebug(telemetrySettings.DebugAddr)
	if err != nil {
		return fmt.Errorf("debug listener: %w", err)
	}

	// Plaintext: services only talk inside the platform's private network.
	// NewClient doesn't connect yet, so the services may start after us.
	// Closed last (defers run after the shutdown below), once nothing uses them.
	conns := map[string]*grpc.ClientConn{}
	for name, addr := range map[string]string{"iam": iamAddr, "storage": storageAddr, "documents": documentsAddr} {
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()), telemetry.DialOption())
		if err != nil {
			return fmt.Errorf("%s client: %w", name, err)
		}
		defer conn.Close()
		conns[name] = conn
	}

	nc, err := gnats.Connect(ctx, settings.NATSURL, "gateway")
	if err != nil {
		return err
	}
	gw, err := gateway.Open(settings, gateway.Services{
		IAM:       iam.NewGRPCClient(conns["iam"]),
		Storage:   storage.NewGRPCClient(conns["storage"]),
		Documents: documents.NewGRPCClient(conns["documents"]),
		Authz:     iam.NewAuthzClient(conns["iam"]),
		NATS:      nc,
	})
	if err != nil {
		nc.Close(context.WithoutCancel(ctx))
		return err
	}
	lis, err := net.Listen("tcp", settings.Addr)
	if err != nil {
		nc.Close(context.WithoutCancel(ctx))
		return err
	}
	server := &http.Server{Handler: gw.Handler, ReadHeaderTimeout: 10 * time.Second}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(lis) }()
	feedCtx, stopFeed := context.WithCancel(context.WithoutCancel(ctx))
	defer stopFeed()
	feedDone := gw.StartFeed(feedCtx)
	slog.Info("gateway started", "http", lis.Addr().String(), "iam", iamAddr, "storage", storageAddr, "documents", documentsAddr, "nats", settings.NATSURL)

	select {
	case <-ctx.Done():
	case err := <-serveErr:
		stopFeed()
		<-feedDone
		nc.Close(context.WithoutCancel(ctx))
		return fmt.Errorf("gateway: %w", err)
	}
	stop() // from here a second Ctrl+C kills the process instead of waiting
	slog.Info("shutting down", "timeout", shutdownTimeout)
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		server.Close()
		slog.Warn("requests cut off at shutdown deadline")
	}
	// Then the WebSockets (hijacked, so Shutdown doesn't wait for them), then
	// NATS, which they publish to.
	stopFeed()
	if err := shutdown.Within(shutdownCtx, func() { <-feedDone }); err != nil {
		slog.Warn("WebSockets cut off at shutdown deadline")
	}
	if err := nc.Close(shutdownCtx); err != nil {
		slog.Warn("closing NATS", "err", err)
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
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
