package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"gyoza/platform/postgres"
	storagecatalogue "gyoza/services/storagecatalogue"
)

func main() {
	if err := run(); err != nil {
		slog.Error("gyoza failed", "err", err)
		os.Exit(1)
	}
}

// run holds the real main so that deferred cleanups (closing the pool) run
// before the process exits; os.Exit skips defers.
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dbURL := os.Getenv("GYOZA_DATABASE_URL")
	if dbURL == "" {
		return errors.New("GYOZA_DATABASE_URL is not set")
	}
	// One pool shared by every service; each still only touches its own schema.
	pool, err := postgres.Connect(ctx, dbURL, "gyoza")
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := storagecatalogue.Migrate(ctx, pool); err != nil {
		return err
	}
	catalogue := storagecatalogue.NewPostgres(pool)
	_ = catalogue // nothing consumes it yet; storage-manager will take it directly

	slog.Info("gyoza started")
	<-ctx.Done()
	slog.Info("shutting down")
	return nil
}
