package ledger_test

import (
	"context"
	"os"
	"testing"

	"github.com/mjcourte/modulith/platform/postgres"
	"github.com/mjcourte/modulith/services/storage/internal/db"
	ledger "github.com/mjcourte/modulith/services/storage/internal/ledger"
)

// TestPostgres runs the shared suite against a real database, e.g.
//
//	MODULITH_TEST_DATABASE_URL=postgres://modulith:modulith@localhost:5432/modulith
//
// Each subtest runs in a transaction that is rolled back, so it leaves no data behind.
func TestPostgres(t *testing.T) {
	url := os.Getenv("MODULITH_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set MODULITH_TEST_DATABASE_URL to run against Postgres")
	}
	pool, err := postgres.Connect(t.Context(), url, "storage-ledger-test") // same settings as production
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := db.Migrate(t.Context(), pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	runServiceTests(t, func(t *testing.T) ledger.Service {
		tx, err := pool.Begin(t.Context())
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		// t.Context() is already cancelled when cleanups run, so roll back with a fresh one.
		t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
		return ledger.NewPostgres(tx)
	})
}
