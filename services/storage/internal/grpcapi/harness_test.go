package grpcapi_test

import (
	"testing"

	"github.com/mjcourte/modulith/services/storage/internal/blob"
	"github.com/mjcourte/modulith/services/storage/internal/dam"
	"github.com/mjcourte/modulith/services/storage/internal/ledger"
	"github.com/mjcourte/modulith/services/storage/internal/manager"
	"github.com/mjcourte/modulith/services/storage/internal/snapshots"
)

// testManager is a Manager on memory fakes, kept so tests can compare the
// client's answers with the fakes' state and play the browser.
type testManager struct {
	*manager.Manager
	blobs  *blob.Memory
	store  *dam.MemoryStore
	ledger *ledger.Memory
	authz  *manager.RuleAuthorizer
}

const testBucket = "modulith-test"

var (
	acme    = ledger.Owner{Kind: ledger.OwnerOrg, ID: "acme"}
	project = ledger.Owner{Kind: ledger.OwnerProject, ID: "p1"}
)

// newTestManager returns a Manager where "member" may write acme's files and
// every user may write their own library.
func newTestManager(t *testing.T) testManager {
	t.Helper()
	tm := testManager{blobs: blob.NewMemory(), store: dam.NewMemoryStore(), ledger: ledger.NewMemory()}
	tm.authz = manager.NewRuleAuthorizer()
	tm.authz.AllowOwner("member", acme, manager.AccessWrite)
	m, err := manager.NewManager(tm.ledger, tm.store, snapshots.NewMemory(), tm.blobs, tm.authz, manager.Config{Bucket: testBucket})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	tm.Manager = m
	return tm
}

// newAssetRequest asks to upload a new 2 KiB PNG to acme's library.
func newAssetRequest() manager.UploadRequest {
	return manager.UploadRequest{Owner: acme, Name: "Logo", Tags: []string{"brand"}, ContentType: "image/png", SizeBytes: 2048}
}

// fileOf returns size bytes that sniff as contentType.
func fileOf(contentType string, size int) []byte {
	headers := map[string]string{"image/png": "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"}
	data := make([]byte, size)
	copy(data, headers[contentType])
	return data
}

// uploadAsset runs the whole upload flow as actor through the Manager.
func uploadAsset(t *testing.T, tm testManager, actor string, req manager.UploadRequest) (dam.Asset, []byte) {
	t.Helper()
	ticket, err := tm.BeginUpload(t.Context(), actor, req)
	if err != nil {
		t.Fatalf("BeginUpload: %v", err)
	}
	data := fileOf(req.ContentType, int(req.SizeBytes))
	if err := tm.blobs.PutSigned(ticket.URL, ticket.Headers["Content-Type"], data); err != nil {
		t.Fatalf("browser upload: %v", err)
	}
	a, err := tm.CompleteUpload(t.Context(), actor, ticket.UploadID)
	if err != nil {
		t.Fatalf("CompleteUpload: %v", err)
	}
	return a, data
}

func ptr[T any](v T) *T { return &v }
