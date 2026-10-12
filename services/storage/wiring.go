package storage

// Setup shared by the binaries hosting storage (cmd/gyoza, cmd/storage), so
// they can't drift apart.

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"gyoza/platform/config"
	"gyoza/services/mediaprocessor"
	"gyoza/services/storage/internal/blob"
	"gyoza/services/storage/internal/dam"
	"gyoza/services/storage/internal/ledger"
	"gyoza/services/storage/internal/manager"
)

// Settings is everything storage reads from the environment.
type Settings struct {
	S3              blob.S3Config
	CreateBucket    bool // create the bucket at startup: local development only
	Manager         Config
	CleanupInterval time.Duration
	ProbeInterval   time.Duration // how often the media probe job looks for new uploads
}

// SettingsFromEnv reads storage's settings. Problems are recorded in env;
// check env.Err() after reading everything.
func SettingsFromEnv(env *config.Env) Settings {
	return Settings{
		S3: blob.S3Config{
			Endpoint:  env.String("GYOZA_S3_ENDPOINT", ""), // empty: AWS itself
			Region:    env.String("GYOZA_S3_REGION", "us-east-1"),
			AccessKey: env.Required("GYOZA_S3_ACCESS_KEY"),
			SecretKey: env.Required("GYOZA_S3_SECRET_KEY"),
			PathStyle: env.Bool("GYOZA_S3_PATH_STYLE", false),
		},
		CreateBucket: env.Bool("GYOZA_S3_CREATE_BUCKET", false),
		Manager: Config{
			Bucket:         env.Required("GYOZA_S3_BUCKET"),
			UploadURLTTL:   env.Duration("GYOZA_UPLOAD_URL_TTL", DefaultUploadURLTTL),
			DownloadURLTTL: env.Duration("GYOZA_DOWNLOAD_URL_TTL", DefaultDownloadURLTTL),
			TrashRetention: env.Duration("GYOZA_TRASH_RETENTION", DefaultTrashRetention),
		},
		CleanupInterval: env.Duration("GYOZA_CLEANUP_INTERVAL", 10*time.Minute),
		ProbeInterval:   env.Duration("GYOZA_PROBE_INTERVAL", 10*time.Second),
	}
}

// Open migrates the schema and builds the Manager on pool and S3.
func Open(ctx context.Context, pool *pgxpool.Pool, s Settings) (*Manager, error) {
	if err := Migrate(ctx, pool); err != nil {
		return nil, err
	}
	blobs, err := blob.NewS3(s.S3)
	if err != nil {
		return nil, err
	}
	if s.CreateBucket {
		if err := blobs.EnsureBucket(ctx, s.Manager.Bucket); err != nil {
			return nil, err
		}
	}
	// No iam yet, so nothing can answer "may this user do that?".
	// Every caller is trusted until it exists: fine while only the platform
	// itself can reach this service, never once browsers can.
	slog.Warn("storage: authorization is allow-all until iam exists")

	m, err := manager.NewManager(ledger.NewPostgres(pool), dam.NewPostgres(pool), blobs, AllowAll{}, s.Manager)
	if err != nil {
		return nil, fmt.Errorf("storage: %w", err)
	}
	return m, nil
}

// StartCleanup runs m's cleanup job in the background until ctx is done. The
// returned channel closes once the job has stopped, so shutdown can wait for
// a run in progress to finish.
func StartCleanup(ctx context.Context, m *Manager, interval time.Duration) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.RunCleanup(ctx, interval)
	}()
	return done
}

// StartMediaProbe runs m's media probe job with p in the background until ctx
// is done. The returned channel closes once the job has stopped.
func StartMediaProbe(ctx context.Context, m *Manager, p mediaprocessor.Prober, interval time.Duration) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.RunMediaProbe(ctx, p, interval)
	}()
	return done
}
