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
	gnats "gyoza/platform/nats"
	"gyoza/platform/postgres"
	"gyoza/platform/shutdown"
	"gyoza/services/documents"
	"gyoza/services/iam"
	"gyoza/services/mediaprocessor"
	"gyoza/services/storage"
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
	storageSettings := storage.SettingsFromEnv(env)
	iamSettings := iam.SettingsFromEnv(env)
	documentsSettings := documents.SettingsFromEnv(env)
	if err := env.Err(); err != nil {
		return err
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})))

	// One pool shared by every service; each still only touches its own schema.
	pool, err := postgres.Connect(ctx, dbURL, "gyoza")
	if err != nil {
		return err
	}
	// Built in this order to break the cycle (storage asks iam who may do
	// what; iam's deletions call storage): iam's store, the authorizer that
	// reads it, storage, then iam's Manager. See services/iam/PLAN.md.
	iamStore, err := iam.OpenStore(ctx, pool)
	if err != nil {
		pool.Close() // nothing is running yet, so this returns promptly
		return err
	}
	authz := iam.NewStorageAuthorizer(iamStore)
	manager, err := storage.Open(ctx, pool, storageSettings, authz)
	if err != nil {
		pool.Close()
		return err
	}
	// No identity provider until the gateway (and SuperTokens) exists.
	iamManager, err := iam.NewManager(iamStore, manager, iam.NoIdentityProvider{}, iamSettings.Manager)
	if err != nil {
		pool.Close()
		return err
	}
	// documents reads and writes through the in-process storage, asks the same
	// iam authorizer, and shares the NATS cluster with every other instance.
	nc, err := gnats.Connect(ctx, documentsSettings.NATSURL, "gyoza")
	if err != nil {
		pool.Close()
		return err
	}
	docs := documents.Open(documentsSettings, manager, authz, nc)

	// Until the gateway exists, the services are reachable over gRPC so
	// they can be called by hand (go tool buf curl, see contracts/README.md).
	lis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		nc.Close(context.WithoutCancel(ctx))
		pool.Close()
		return err
	}
	// The monolith runs ffprobe in-process (it gets an empty environment, see
	// mediaprocessor.FFprobe). Without it, uploads stay queued until a run
	// with ffprobe picks them up.
	prober, err := mediaprocessor.NewFFprobe()
	if err != nil {
		slog.Warn("media probing off: ffprobe not found", "err", err) // prober is nil
	}

	server := grpc.NewServer()
	storage.RegisterGRPC(server, manager)
	iam.RegisterGRPC(server, iamManager, authz)
	documents.RegisterGRPC(server, docs)
	if prober != nil {
		mediaprocessor.RegisterGRPC(server, prober)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(lis) }()

	// Background jobs share a context, cancelled at shutdown before the pool
	// closes, since a run in progress still needs the database.
	jobsCtx, stopJobs := context.WithCancel(context.WithoutCancel(ctx))
	defer stopJobs()
	cleanupDone := storage.StartCleanup(jobsCtx, manager, storageSettings.CleanupInterval)
	iamCleanupDone := iam.StartCleanup(jobsCtx, iamManager, iamSettings.CleanupInterval)
	var probeDone <-chan struct{}
	if prober == nil {
		stopped := make(chan struct{})
		close(stopped) // nothing to wait for at shutdown
		probeDone = stopped
	} else {
		probeDone = storage.StartMediaProbe(jobsCtx, manager, prober, storageSettings.ProbeInterval)
	}
	consumerDone := documents.StartConsumer(jobsCtx, docs, nc, documentsSettings.Consumer)
	jobsDone := func() { <-cleanupDone; <-iamCleanupDone; <-probeDone; <-consumerDone }
	slog.Info("gyoza started", "grpc", lis.Addr().String(), "bucket", storageSettings.Manager.Bucket, "replica", documentsSettings.Manager.Replica)

	select {
	case <-ctx.Done():
	case err := <-serveErr:
		stopJobs()
		jobsDone()
		docs.Close(context.WithoutCancel(ctx))
		nc.Close(context.WithoutCancel(ctx))
		pool.Close()
		return fmt.Errorf("grpc server: %w", err)
	}
	stop() // from here a second Ctrl+C kills the process instead of waiting
	slog.Info("shutting down", "timeout", shutdownTimeout)

	// ctx is cancelled now, so shutdown work gets its own deadline.
	// WithoutCancel keeps ctx's values but drops its cancellation.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()

	// Stop in reverse order of startup: the server, the background jobs (the
	// documents consumer among them), then documents saves every open
	// document (through storage, announcing on NATS), then NATS, then the
	// pool, which all of them need until they've stopped.
	if err := shutdown.Within(shutdownCtx, server.GracefulStop); err != nil {
		server.Stop()
		slog.Warn("grpc calls cut off at shutdown deadline")
	}
	stopJobs()
	if err := shutdown.Within(shutdownCtx, jobsDone); err != nil {
		slog.Warn("background jobs cut off at shutdown deadline")
	}
	// Unsaved documents aren't lost if this fails: their deltas stay in the
	// stream until a snapshot covering them is stored.
	if err := docs.Close(shutdownCtx); err != nil {
		slog.Error("saving open documents", "err", err)
	}
	if err := nc.Close(shutdownCtx); err != nil {
		slog.Warn("closing NATS", "err", err)
	}
	if err := shutdown.Within(shutdownCtx, pool.Close); err != nil {
		return err
	}
	slog.Info("stopped cleanly")
	return nil
}
