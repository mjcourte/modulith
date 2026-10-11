package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gyoza/platform/config"
	"gyoza/platform/postgres"
	"gyoza/platform/shutdown"
	storageledger "gyoza/services/storageledger"
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
	ledger := storageledger.NewPostgres(pool)
	_ = ledger // nothing consumes it yet; storage-manager will take it directly

	slog.Info("gyoza started")
	<-ctx.Done()
	stop() // from here a second Ctrl+C kills the process instead of waiting
	slog.Info("shutting down", "timeout", shutdownTimeout)

	// ctx is cancelled now, so shutdown work gets its own deadline.
	// WithoutCancel keeps ctx's values but drops its cancellation.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()

	// Stop in reverse order of startup: servers first (once there are any),
	// then the pool, because in-flight requests still need it.
	if err := shutdown.Within(shutdownCtx, pool.Close); err != nil {
		return err
	}
	slog.Info("stopped cleanly")
	return nil
}
