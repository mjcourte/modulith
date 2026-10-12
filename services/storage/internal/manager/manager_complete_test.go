package manager_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"gyoza/services/storage/internal/blob"
	"gyoza/services/storage/internal/dam"
	"gyoza/services/storage/internal/ledger"
	sm "gyoza/services/storage/internal/manager"
)

// fileOf returns size bytes that sniff as contentType: a real header padded
// with zeros.
func fileOf(contentType string, size int) []byte {
	headers := map[string]string{
		"image/png":  "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR",
		"image/jpeg": "\xff\xd8\xff\xe0\x00\x10JFIF\x00",
		"zip":        "PK\x03\x04\x14\x00\x00\x00\x08\x00",
	}
	data := make([]byte, size)
	copy(data, headers[contentType])
	return data
}

// beginAndPut starts an upload as actor and has the browser PUT a matching file.
func beginAndPut(t *testing.T, tm testManager, actor string, req sm.UploadRequest) sm.UploadTicket {
	t.Helper()
	ticket, err := tm.BeginUpload(t.Context(), actor, req)
	if err != nil {
		t.Fatalf("BeginUpload: %v", err)
	}
	data := fileOf(req.ContentType, int(req.SizeBytes))
	if err := tm.blobs.PutSigned(ticket.URL, ticket.Headers["Content-Type"], data); err != nil {
		t.Fatalf("browser upload: %v", err)
	}
	return ticket
}

// liveLedgerObjects counts acme's ledger objects that aren't deleted.
func liveLedgerObjects(t *testing.T, tm testManager) []ledger.Object {
	t.Helper()
	page, err := tm.ledger.List(t.Context(), ledger.ListQuery{Owner: acme})
	if err != nil {
		t.Fatalf("ledger List: %v", err)
	}
	return page.Objects
}

func acmeAssets(t *testing.T, tm testManager) []dam.Asset {
	t.Helper()
	page, err := tm.store.ListAssets(t.Context(), dam.AssetQuery{Owner: acme})
	if err != nil {
		t.Fatalf("ListAssets: %v", err)
	}
	return page.Assets
}

func TestCompleteUploadNewAsset(t *testing.T) {
	tm := newTestManager(t)
	ctx := t.Context()
	ticket := beginAndPut(t, tm, "member", newAssetRequest())

	a, err := tm.CompleteUpload(ctx, "member", ticket.UploadID)
	if err != nil {
		t.Fatalf("CompleteUpload: %v", err)
	}
	if a.Name != "Logo" || a.Owner != acme || a.CreatedBy != "member" || a.Category != dam.CategoryImage || a.CurrentVersion != 1 {
		t.Errorf("asset = %+v", a)
	}

	// The version points at a ledger object describing exactly the uploaded file.
	vs := mustListVersions(t, tm.store, a.ID)
	obj, err := tm.ledger.Get(ctx, vs[0].ObjectID)
	if err != nil {
		t.Fatalf("ledger Get: %v", err)
	}
	u, _ := tm.store.GetUpload(ctx, ticket.UploadID)
	if obj.Kind != ledger.KindAsset || obj.Owner != acme || obj.CreatedBy != "member" ||
		obj.Bucket != testBucket || obj.Key != u.Key || obj.SizeBytes != 2048 || obj.ContentType != "image/png" {
		t.Errorf("ledger object = %+v; want it to describe the uploaded file at %s", obj, u.Key)
	}
	if u.CompletedAt == nil || u.ResultAssetID != a.ID || u.ObjectID != obj.ID {
		t.Errorf("session after completing = %+v", u)
	}
}

func TestCompleteUploadNewVersion(t *testing.T) {
	tm := newTestManager(t)
	ctx := t.Context()
	first := beginAndPut(t, tm, "member", newAssetRequest())
	a, err := tm.CompleteUpload(ctx, "member", first.UploadID)
	if err != nil {
		t.Fatalf("CompleteUpload: %v", err)
	}

	second := beginAndPut(t, tm, "member", sm.UploadRequest{AssetID: a.ID, ContentType: "image/jpeg", SizeBytes: 4096})
	updated, err := tm.CompleteUpload(ctx, "member", second.UploadID)
	if err != nil {
		t.Fatalf("CompleteUpload (version): %v", err)
	}
	if updated.ID != a.ID || updated.CurrentVersion != 2 || updated.ContentType != "image/jpeg" {
		t.Errorf("asset = %+v; want version 2 (jpeg) current", updated)
	}
	if objs := liveLedgerObjects(t, tm); len(objs) != 2 {
		t.Errorf("ledger objects = %d, want 2 (one per version)", len(objs))
	}
}

// Completing again (a retry, a timeout, a double click) changes nothing.
func TestCompleteUploadIsIdempotent(t *testing.T) {
	tm := newTestManager(t)
	ticket := beginAndPut(t, tm, "member", newAssetRequest())
	first, err := tm.CompleteUpload(t.Context(), "member", ticket.UploadID)
	if err != nil {
		t.Fatalf("CompleteUpload: %v", err)
	}
	again, err := tm.CompleteUpload(t.Context(), "member", ticket.UploadID)
	if err != nil || again.ID != first.ID {
		t.Errorf("second CompleteUpload = %s, %v; want %s", again.ID, err, first.ID)
	}
	if n := len(liveLedgerObjects(t, tm)); n != 1 {
		t.Errorf("ledger objects = %d, want 1", n)
	}
	if n := len(acmeAssets(t, tm)); n != 1 {
		t.Errorf("assets = %d, want 1", n)
	}
}

// A previous attempt that recorded the file in the ledger, then crashed before
// finishing, is resumed with that same object instead of creating another.
func TestCompleteUploadResumesRecordedObject(t *testing.T) {
	tm := newTestManager(t)
	ctx := t.Context()
	ticket := beginAndPut(t, tm, "member", newAssetRequest())
	u, _ := tm.store.GetUpload(ctx, ticket.UploadID)

	// Simulate the earlier attempt: the ledger object exists, nothing else happened.
	earlier, err := tm.ledger.Create(ctx, ledger.Object{ID: u.ObjectID, Owner: acme, CreatedBy: "member", Kind: ledger.KindAsset,
		Bucket: u.Bucket, Key: u.Key, SizeBytes: u.SizeBytes, ContentType: u.ContentType})
	if err != nil {
		t.Fatalf("ledger Create: %v", err)
	}

	a, err := tm.CompleteUpload(ctx, "member", u.ID)
	if err != nil {
		t.Fatalf("CompleteUpload: %v", err)
	}
	if vs := mustListVersions(t, tm.store, a.ID); vs[0].ObjectID != earlier.ID {
		t.Errorf("version object = %s, want the earlier %s", vs[0].ObjectID, earlier.ID)
	}
	all, err := tm.ledger.List(ctx, ledger.ListQuery{Owner: acme, IncludeDeleted: true})
	if err != nil || len(all.Objects) != 1 {
		t.Errorf("ledger objects including deleted = %d, %v; want 1", len(all.Objects), err)
	}
}

// Asking for the result again after the session expired still gets it.
func TestCompleteUploadResultOutlivesExpiry(t *testing.T) {
	tm := newTestManagerWith(t, sm.Config{Bucket: testBucket, UploadURLTTL: 50 * time.Millisecond}, nil)
	ticket := beginAndPut(t, tm, "member", newAssetRequest())
	first, err := tm.CompleteUpload(t.Context(), "member", ticket.UploadID)
	if err != nil {
		t.Fatalf("CompleteUpload: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	later, err := tm.CompleteUpload(t.Context(), "member", ticket.UploadID)
	if err != nil || later.ID != first.ID {
		t.Errorf("CompleteUpload after expiry = %s, %v; want %s", later.ID, err, first.ID)
	}
}

// If storage reports a different file than was signed for, nothing is recorded.
func TestCompleteUploadDistrustsStorage(t *testing.T) {
	tm := newTestManagerWith(t, sm.Config{Bucket: testBucket}, func(b *blob.Memory) blob.Store { return lyingBlobs{b} })
	ticket := beginAndPut(t, tm, "member", newAssetRequest())
	if _, err := tm.CompleteUpload(t.Context(), "member", ticket.UploadID); !errors.Is(err, dam.ErrInvalidUpload) {
		t.Errorf("CompleteUpload = %v, want ErrInvalidUpload", err)
	}
	if n := len(liveLedgerObjects(t, tm)); n != 0 {
		t.Errorf("ledger objects = %d, want 0", n)
	}
}

// gatedBlobs holds every Stat call until n have arrived, then lets them all
// through together, so n CompleteUpload calls really do overlap. Embedding
// *blob.Memory gives it all of Memory's methods; it only redefines Stat.
type gatedBlobs struct {
	*blob.Memory
	arrived *barrier
}

func (g *gatedBlobs) Stat(ctx context.Context, bucket, key string) (blob.Info, error) {
	g.arrived.arrive()
	g.arrived.await() // until all n calls are here
	return g.Memory.Stat(ctx, bucket, key)
}

// barrier opens once n goroutines have arrived. Waiting gives up after a few
// seconds, so a test that miscounts fails its assertions instead of hanging.
type barrier struct {
	mu      sync.Mutex
	pending int
	open    chan struct{}
}

func newBarrier(n int) *barrier {
	return &barrier{pending: n, open: make(chan struct{})}
}

func (b *barrier) arrive() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pending--
	if b.pending == 0 {
		close(b.open)
	}
}

func (b *barrier) await() {
	select {
	case <-b.open:
	case <-time.After(5 * time.Second):
	}
}

// lyingBlobs reports a different size than what's stored, as a misbehaving
// storage backend might.
type lyingBlobs struct{ *blob.Memory }

func (l lyingBlobs) Stat(ctx context.Context, bucket, key string) (blob.Info, error) {
	info, err := l.Memory.Stat(ctx, bucket, key)
	info.Size++
	return info, err
}

// gatedStore holds every CompleteUpload until n have arrived, so all the
// overlapping calls have recorded the file in the ledger before any of them
// can finish the upload.
type gatedStore struct {
	*dam.MemoryStore
	arrived *barrier
}

func (g *gatedStore) CompleteUpload(ctx context.Context, id string, v dam.Version) (dam.Asset, error) {
	g.arrived.arrive()
	g.arrived.await()
	return g.MemoryStore.CompleteUpload(ctx, id, v)
}

// When overlapping calls all record the file in the ledger before any of
// them finishes, there is still exactly one ledger object, deleted or not:
// they all used the ID the session chose.
func TestCompleteUploadRecordsOneObject(t *testing.T) {
	const calls = 10
	tm := newTestManager(t) // for the fakes and the browser
	store := &gatedStore{MemoryStore: tm.store, arrived: newBarrier(calls)}
	m, err := sm.NewManager(tm.ledger, store, tm.blobs, tm.authz, sm.Config{Bucket: testBucket})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	ticket := beginAndPut(t, tm, "member", newAssetRequest())

	var wg sync.WaitGroup
	for range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.CompleteUpload(t.Context(), "member", ticket.UploadID); err != nil {
				t.Errorf("CompleteUpload: %v", err)
			}
		}()
	}
	wg.Wait()

	all, err := tm.ledger.List(t.Context(), ledger.ListQuery{Owner: acme, IncludeDeleted: true})
	if err != nil || len(all.Objects) != 1 {
		t.Fatalf("ledger objects including deleted = %d, %v; want 1", len(all.Objects), err)
	}
	u, _ := tm.store.GetUpload(t.Context(), ticket.UploadID)
	if all.Objects[0].ID != u.ObjectID {
		t.Errorf("ledger object %s isn't the session's %s", all.Objects[0].ID, u.ObjectID)
	}
}

// Many overlapping completions of one upload end with one asset and one live
// ledger object, and all of them report that asset.
func TestCompleteUploadOverlapping(t *testing.T) {
	const calls = 20
	gate := &gatedBlobs{arrived: newBarrier(calls)}
	tm := newTestManagerWith(t, sm.Config{Bucket: testBucket}, func(b *blob.Memory) blob.Store {
		gate.Memory = b
		return gate
	})
	ticket := beginAndPut(t, tm, "member", newAssetRequest())

	ids := make([]string, calls)
	errs := make([]error, calls)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // release them all at once
			a, err := tm.CompleteUpload(t.Context(), "member", ticket.UploadID)
			ids[i], errs[i] = a.ID, err
		}()
	}
	close(start)
	wg.Wait()

	for i := range calls {
		if errs[i] != nil || ids[i] != ids[0] {
			t.Errorf("call %d = %s, %v; want %s", i, ids[i], errs[i], ids[0])
		}
	}
	if n := len(liveLedgerObjects(t, tm)); n != 1 {
		t.Errorf("live ledger objects = %d, want 1", n)
	}
	if n := len(acmeAssets(t, tm)); n != 1 {
		t.Errorf("assets = %d, want 1", n)
	}
}

func TestCompleteUploadRejects(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, tm testManager) (actor, uploadID string)
		want  error
	}{
		{"missing upload", func(t *testing.T, tm testManager) (string, string) {
			return "member", uuid.NewString()
		}, dam.ErrUploadNotFound},
		{"another user", func(t *testing.T, tm testManager) (string, string) {
			tm.authz.AllowOwner("colleague", acme, sm.AccessWrite) // may write, but didn't begin it
			return "colleague", beginAndPut(t, tm, "member", newAssetRequest()).UploadID
		}, sm.ErrForbidden},
		{"creator lost access", func(t *testing.T, tm testManager) (string, string) {
			id := beginAndPut(t, tm, "member", newAssetRequest()).UploadID
			tm.authz.AllowOwner("member", acme, sm.AccessNone)
			return "member", id
		}, sm.ErrForbidden},
		{"file not uploaded", func(t *testing.T, tm testManager) (string, string) {
			ticket, err := tm.BeginUpload(t.Context(), "member", newAssetRequest())
			if err != nil {
				t.Fatalf("BeginUpload: %v", err)
			}
			return "member", ticket.UploadID
		}, sm.ErrNotUploaded},
		{"bytes aren't a PNG", func(t *testing.T, tm testManager) (string, string) {
			req := newAssetRequest()
			ticket, err := tm.BeginUpload(t.Context(), "member", req)
			if err != nil {
				t.Fatalf("BeginUpload: %v", err)
			}
			zip := fileOf("zip", int(req.SizeBytes)) // right size and header, wrong content
			if err := tm.blobs.PutSigned(ticket.URL, "image/png", zip); err != nil {
				t.Fatalf("browser upload: %v", err)
			}
			return "member", ticket.UploadID
		}, sm.ErrContentMismatch},
		{"expired", func(t *testing.T, tm testManager) (string, string) {
			u, err := tm.store.CreateUpload(t.Context(), dam.Upload{
				Owner: acme, CreatedBy: "member", Name: "Late", Bucket: testBucket, Key: "library/asset/" + uuid.NewString(),
				ContentType: "image/png", SizeBytes: 10, ExpiresAt: time.Now().Add(-time.Minute),
			})
			if err != nil {
				t.Fatalf("CreateUpload: %v", err)
			}
			return "member", u.ID
		}, dam.ErrUploadExpired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tm := newTestManager(t)
			actor, id := tt.setup(t, tm)
			if _, err := tm.CompleteUpload(t.Context(), actor, id); !errors.Is(err, tt.want) {
				t.Errorf("CompleteUpload = %v, want %v", err, tt.want)
			}
			if n := len(liveLedgerObjects(t, tm)); n != 0 {
				t.Errorf("refused completion left %d ledger objects", n)
			}
			if n := len(acmeAssets(t, tm)); n != 0 {
				t.Errorf("refused completion created %d assets", n)
			}
		})
	}
}
