package directory_test

import (
	"context"
	"os"
	"testing"

	"github.com/mjcourte/modulith/platform/postgres"
	"github.com/mjcourte/modulith/services/iam/internal/db"
	"github.com/mjcourte/modulith/services/iam/internal/directory"
)

// TestPostgresStore runs the shared store suite against a real database, e.g.
//
//	MODULITH_TEST_DATABASE_URL=postgres://modulith:modulith@localhost:5432/modulith
//
// Each subtest runs in a transaction that is rolled back; the Store's own
// transactions become savepoints inside it.
func TestPostgresStore(t *testing.T) {
	url := os.Getenv("MODULITH_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set MODULITH_TEST_DATABASE_URL to run against Postgres")
	}
	pool, err := postgres.Connect(t.Context(), url, "iam-test")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := db.Migrate(t.Context(), pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	runStoreTests(t, func(t *testing.T) directory.Store {
		tx, err := pool.Begin(t.Context())
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
		return directory.NewPostgres(tx)
	})
}
