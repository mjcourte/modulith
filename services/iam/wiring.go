package iam

// Setup shared by the binaries hosting iam (cmd/gyoza, cmd/iam), so they
// can't drift apart.

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"gyoza/platform/config"
)

// Settings is everything iam reads from the environment.
type Settings struct {
	Manager         Config
	CleanupInterval time.Duration // how often unfinished deletions, expired invites and pending shares are dealt with
}

// SettingsFromEnv reads iam's settings. Problems are recorded in env; check
// env.Err() after reading everything.
func SettingsFromEnv(env *config.Env) Settings {
	return Settings{
		Manager: Config{
			InviteTTL: env.Duration("GYOZA_INVITE_TTL", DefaultInviteTTL),
			ShareTTL:  time.Duration(env.PositiveInt("GYOZA_SHARE_EXPIRY_DAYS", int64(DefaultShareTTL/(24*time.Hour)))) * 24 * time.Hour,
		},
		CleanupInterval: env.Duration("GYOZA_IAM_CLEANUP_INTERVAL", 10*time.Minute),
	}
}

// OpenStore migrates the schema and returns the Postgres Store on pool. The
// binaries build it first: storage's Authorizer reads it, and iam's Manager
// (which calls storage) comes after storage.
func OpenStore(ctx context.Context, pool *pgxpool.Pool) (Store, error) {
	if err := Migrate(ctx, pool); err != nil {
		return nil, err
	}
	return NewPostgres(pool), nil
}

// StartCleanup runs m's cleanup loop (unfinished deletions, expired invites)
// in the background until ctx is done. The returned channel closes once it
// has stopped, so shutdown can wait for a run in progress.
func StartCleanup(ctx context.Context, m *Manager, interval time.Duration) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.RunCleanup(ctx, interval)
	}()
	return done
}
