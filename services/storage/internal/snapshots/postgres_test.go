package snapshots_test

import (
	"context"
	"os"
	"testing"

	"gyoza/platform/postgres"
	"gyoza/services/storage/internal/db"
	"gyoza/services/storage/internal/snapshots"
)

// TestPostgres runs the suite against a real database (GYOZA_TEST_DATABASE_URL).
// Each subtest runs in a rolled-back transaction, except the concurrency test,
// which needs the pool; it uses fresh project IDs and leaves its rows behind.
func TestPostgres(t *testing.T) {
	url := os.Getenv("GYOZA_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set GYOZA_TEST_DATABASE_URL to run against Postgres")
	}
	pool, err := postgres.Connect(t.Context(), url, "storage-snapshots-test")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := db.Migrate(t.Context(), pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	runStoreTests(t, func(t *testing.T) snapshots.Store {
		tx, err := pool.Begin(t.Context())
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
		return snapshots.NewPostgres(tx)
	}, false)
	t.Run("ConcurrentCompletesOnPool", func(t *testing.T) {
		testSnapshotConcurrentCompletes(t, snapshots.NewPostgres(pool))
	})
}
