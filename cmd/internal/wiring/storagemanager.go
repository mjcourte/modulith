// Package wiring is setup shared by the binaries in cmd/: reading a service's
// settings from the environment and building it from its dependencies. Being
// under cmd/internal, only cmd/ can import it; services never do.
package wiring

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"gyoza/platform/blob"
	"gyoza/platform/config"
	"gyoza/services/storageledger"
	"gyoza/services/storagemanager"
)

// StorageManagerSettings is everything storagemanager reads from the environment.
type StorageManagerSettings struct {
	S3              blob.S3Config
	CreateBucket    bool // create the bucket at startup: local development only
	Manager         storagemanager.Config
	CleanupInterval time.Duration
}

// StorageManagerFromEnv reads storagemanager's settings. Problems are recorded
// in env; check env.Err() after reading everything.
func StorageManagerFromEnv(env *config.Env) StorageManagerSettings {
	return StorageManagerSettings{
		S3: blob.S3Config{
			Endpoint:  env.String("GYOZA_S3_ENDPOINT", ""), // empty: AWS itself
			Region:    env.String("GYOZA_S3_REGION", "us-east-1"),
			AccessKey: env.Required("GYOZA_S3_ACCESS_KEY"),
			SecretKey: env.Required("GYOZA_S3_SECRET_KEY"),
			PathStyle: env.Bool("GYOZA_S3_PATH_STYLE", false),
		},
		CreateBucket: env.Bool("GYOZA_S3_CREATE_BUCKET", false),
		Manager: storagemanager.Config{
			Bucket:         env.Required("GYOZA_S3_BUCKET"),
			UploadURLTTL:   env.Duration("GYOZA_UPLOAD_URL_TTL", storagemanager.DefaultUploadURLTTL),
			DownloadURLTTL: env.Duration("GYOZA_DOWNLOAD_URL_TTL", storagemanager.DefaultDownloadURLTTL),
			TrashRetention: env.Duration("GYOZA_TRASH_RETENTION", storagemanager.DefaultTrashRetention),
		},
		CleanupInterval: env.Duration("GYOZA_CLEANUP_INTERVAL", 10*time.Minute),
	}
}

// NewStorageManager migrates storagemanager's schema and builds its Manager
// on pool, S3 and the given ledger (in-process or a gRPC client).
func NewStorageManager(ctx context.Context, pool *pgxpool.Pool, ledger storageledger.Service, s StorageManagerSettings) (*storagemanager.Manager, error) {
	if err := storagemanager.Migrate(ctx, pool); err != nil {
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
	// No usermanager yet, so nothing can answer "may this user do that?".
	// Every caller is trusted until it exists: fine while only the platform
	// itself can reach this service, never once browsers can.
	slog.Warn("storagemanager: authorization is allow-all until usermanager exists")
	m, err := storagemanager.NewManager(ledger, storagemanager.NewPostgres(pool), blobs, storagemanager.AllowAll{}, s.Manager)
	if err != nil {
		return nil, fmt.Errorf("storagemanager: %w", err)
	}
	return m, nil
}

// StartCleanup runs m's cleanup job in the background until ctx is done. The
// returned channel closes once the job has stopped, so shutdown can wait for
// a run in progress to finish.
func StartCleanup(ctx context.Context, m *storagemanager.Manager, interval time.Duration) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.RunCleanup(ctx, interval)
	}()
	return done
}
