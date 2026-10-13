package snapshots_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"gyoza/internal/engine"
	"gyoza/platform/postgres"
	"gyoza/services/documents/internal/snapshots"
	"gyoza/services/documents/internal/snapshots/snapshotstest"
	"gyoza/services/storage"
)

// anyProject is storage's Authorizer for these tests: every project exists
// (owned by a user), which is all snapshot calls ask.
type anyProject struct{ storage.AllowAll }

func (anyProject) ProjectOwner(ctx context.Context, projectID string) (storage.Owner, error) {
	if projectID == "" {
		return storage.Owner{}, fmt.Errorf("%w: no project", storage.ErrForbidden)
	}
	return storage.Owner{Kind: storage.OwnerUser, ID: "owner"}, nil
}

// memoryTransfer plays the browser against storage.Memory's fake S3.
type memoryTransfer struct{ m *storage.Memory }

func (t memoryTransfer) Put(_ context.Context, url string, headers map[string]string, data []byte) error {
	return t.m.Upload(url, headers["Content-Type"], data)
}

func (t memoryTransfer) Get(_ context.Context, url string) ([]byte, error) {
	return t.m.Download(url)
}

// The durable Store over storage's real logic, with in-memory data and S3.
func TestStorageStore(t *testing.T) {
	snapshotstest.RunSuite(t, func(t *testing.T) snapshots.Store {
		m, err := storage.NewMemory(anyProject{}, storage.Config{Bucket: "test"})
		if err != nil {
			t.Fatal(err)
		}
		return snapshots.NewStorageStore(m, memoryTransfer{m})
	})
}

// The same over the real stack: storage on Postgres and RustFS, the bytes
// over HTTP. Needs GYOZA_TEST_DATABASE_URL and GYOZA_TEST_S3_ENDPOINT (see
// CLAUDE.md). It commits to the dev database and bucket: every run uses fresh
// project IDs, and retention cleans their snapshots up eventually.
func TestStorageStoreOnS3(t *testing.T) {
	dbURL, s3 := os.Getenv("GYOZA_TEST_DATABASE_URL"), os.Getenv("GYOZA_TEST_S3_ENDPOINT")
	if dbURL == "" || s3 == "" {
		t.Skip("set GYOZA_TEST_DATABASE_URL and GYOZA_TEST_S3_ENDPOINT to run against Postgres and S3")
	}
	pool, err := postgres.Connect(t.Context(), dbURL, "documents-snapshots-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	m, err := storage.Open(t.Context(), pool, storage.Settings{
		S3: storage.S3Config{
			Endpoint:  s3,
			AccessKey: envOr("GYOZA_TEST_S3_ACCESS_KEY", "gyoza"),
			SecretKey: envOr("GYOZA_TEST_S3_SECRET_KEY", "gyoza-dev-secret"),
			PathStyle: true,
		},
		CreateBucket: true,
		Manager:      storage.Config{Bucket: "gyoza-test"},
	}, anyProject{})
	if err != nil {
		t.Fatal(err)
	}
	transfer := snapshots.NewHTTPTransfer(storage.DefaultMaxSnapshotBytes)
	snapshotstest.RunSuite(t, func(*testing.T) snapshots.Store { return snapshots.NewStorageStore(m, transfer) })
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// A project storage doesn't know (never created, or deleted) is ErrNoProject:
// documents drops its deltas instead of retrying them forever.
func TestStorageStoreUnknownProject(t *testing.T) {
	m, err := storage.NewMemory(storage.AllowAll{}, storage.Config{Bucket: "test"}) // knows no projects
	if err != nil {
		t.Fatal(err)
	}
	s := snapshots.NewStorageStore(m, memoryTransfer{m})
	snap := engine.Snapshot{Format: engine.WireNative, Version: 1, Payload: []byte("x")}
	if _, err := s.Put(context.Background(), "gone", snap, 1, snapshots.Edit{}); !errors.Is(err, snapshots.ErrNoProject) {
		t.Errorf("got %v, want ErrNoProject", err)
	}
}
