package manager_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"gyoza/platform/blob"
	"gyoza/services/storage/internal/dam"
	"gyoza/services/storage/internal/ledger"
	sm "gyoza/services/storage/internal/manager"
)

// testManager is a Manager built from memory fakes, with the fakes kept so
// tests can look behind it (play the browser, inspect the ledger, ...).
type testManager struct {
	*sm.Manager // embedded: tm.BeginUpload(...) calls the Manager's method
	blobs       *blob.Memory
	store       *dam.MemoryStore
	ledger      *ledger.Memory
	authz       *sm.RuleAuthorizer
}

const testBucket = "gyoza-test"

// newTestManager returns a Manager where "member" may write acme's files and
// every user may write their own library.
func newTestManager(t *testing.T) testManager {
	t.Helper()
	return newTestManagerWith(t, sm.Config{Bucket: testBucket}, nil)
}

// newTestManagerWith is newTestManager with a custom config, and optionally a
// wrapper around the memory blobs (to make storage misbehave in a controlled
// way). tm.blobs stays the plain fake, so tests can still play the browser.
func newTestManagerWith(t *testing.T, cfg sm.Config, wrap func(*blob.Memory) blob.Store) testManager {
	t.Helper()
	tm := testManager{
		blobs:  blob.NewMemory(),
		store:  dam.NewMemoryStore(),
		ledger: ledger.NewMemory(),
		authz:  sm.NewRuleAuthorizer(),
	}
	tm.authz.AllowOwner("member", acme, sm.AccessWrite)
	var blobs blob.Store = tm.blobs
	if wrap != nil {
		blobs = wrap(tm.blobs)
	}
	m, err := sm.NewManager(tm.ledger, tm.store, blobs, tm.authz, cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	tm.Manager = m
	return tm
}

// newAssetRequest asks to upload a new 2 KiB PNG to acme's library.
func newAssetRequest() sm.UploadRequest {
	return sm.UploadRequest{Owner: acme, Name: "Logo", Tags: []string{"brand"}, ContentType: "image/png", SizeBytes: 2048}
}

// pendingUploads lists every pending session, by asking for sessions that
// will have expired a year from now.
func pendingUploads(t *testing.T, s dam.Store) []dam.Upload {
	t.Helper()
	ups, err := s.ListExpiredUploads(t.Context(), time.Now().AddDate(1, 0, 0), dam.MaxPageSize)
	if err != nil {
		t.Fatalf("ListExpiredUploads: %v", err)
	}
	return ups
}

func TestNewManagerConfig(t *testing.T) {
	deps := func() (ledger.Service, dam.Store, blob.Store, sm.Authorizer) {
		return ledger.NewMemory(), dam.NewMemoryStore(), blob.NewMemory(), sm.AllowAll{}
	}
	l, s, b, a := deps()
	m, err := sm.NewManager(l, s, b, a, sm.Config{Bucket: "b"})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	want := sm.Config{Bucket: "b", UploadURLTTL: sm.DefaultUploadURLTTL, DownloadURLTTL: sm.DefaultDownloadURLTTL, MaxUploadBytes: sm.DefaultMaxUploadBytes, TrashRetention: sm.DefaultTrashRetention}
	if got := m.Config(); got != want {
		t.Errorf("Config() = %+v, want defaults %+v", got, want)
	}

	bad := map[string]sm.Config{
		"no bucket":             {},
		"upload TTL over 7d":    {Bucket: "b", UploadURLTTL: 8 * 24 * time.Hour},
		"negative download TTL": {Bucket: "b", DownloadURLTTL: -time.Second},
		"max over 5 GiB":        {Bucket: "b", MaxUploadBytes: sm.MaxSinglePutBytes + 1},
		"trash kept under 1h":   {Bucket: "b", TrashRetention: time.Minute},
	}
	for name, cfg := range bad {
		if _, err := sm.NewManager(l, s, b, a, cfg); err == nil {
			t.Errorf("%s: NewManager succeeded, want an error", name)
		}
	}
	if _, err := sm.NewManager(nil, s, b, a, sm.Config{Bucket: "b"}); err == nil {
		t.Error("NewManager without a ledger succeeded, want an error")
	}
}

func TestBeginUploadNewAsset(t *testing.T) {
	tm := newTestManager(t)
	before := time.Now()
	ticket, err := tm.BeginUpload(t.Context(), "member", newAssetRequest())
	if err != nil {
		t.Fatalf("BeginUpload: %v", err)
	}
	if ticket.Method != "PUT" || ticket.Headers["Content-Type"] != "image/png" || ticket.UploadID == "" {
		t.Errorf("ticket = %+v", ticket)
	}
	if want := before.Add(sm.DefaultUploadURLTTL); ticket.ExpiresAt.Before(want) || ticket.ExpiresAt.After(want.Add(time.Minute)) {
		t.Errorf("ExpiresAt = %v, want about %v", ticket.ExpiresAt, want)
	}

	// The browser can use the ticket as given.
	if err := tm.blobs.PutSigned(ticket.URL, ticket.Headers["Content-Type"], make([]byte, 2048)); err != nil {
		t.Errorf("uploading with the ticket: %v", err)
	}

	u, err := tm.store.GetUpload(t.Context(), ticket.UploadID)
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if u.Owner != acme || u.CreatedBy != "member" || u.Bucket != testBucket || u.Name != "Logo" ||
		!strings.HasPrefix(u.Key, "library/asset/") || u.CompletedAt != nil {
		t.Errorf("session = %+v", u)
	}

	// Nothing reaches the DAM or the ledger until CompleteUpload.
	if page, _ := tm.store.ListAssets(t.Context(), dam.AssetQuery{Owner: acme}); len(page.Assets) != 0 {
		t.Errorf("BeginUpload created %d assets", len(page.Assets))
	}
	if objs, _ := tm.ledger.List(t.Context(), ledger.ListQuery{Owner: acme}); len(objs.Objects) != 0 {
		t.Errorf("BeginUpload created %d ledger objects", len(objs.Objects))
	}
}

// Keys follow the README layout and never contain user, org or file names.
func TestBeginUploadKeyLayout(t *testing.T) {
	tm := newTestManager(t)
	tm.authz.AllowProject("member", "p1", sm.AccessWrite)
	req := newAssetRequest()
	req.ProjectID = "p1"
	ticket, err := tm.BeginUpload(t.Context(), "member", req)
	if err != nil {
		t.Fatalf("BeginUpload: %v", err)
	}
	u, _ := tm.store.GetUpload(t.Context(), ticket.UploadID)
	parts := strings.Split(u.Key, "/")
	if len(parts) != 4 || parts[0] != "projects" || parts[1] != "p1" || parts[2] != "asset" {
		t.Fatalf("key %q, want projects/p1/asset/{id}", u.Key)
	}
	if _, err := uuid.Parse(parts[3]); err != nil {
		t.Errorf("key ends in %q, want a generated ID", parts[3])
	}
	if strings.Contains(u.Key, "acme") || strings.Contains(u.Key, "member") || strings.Contains(u.Key, "Logo") {
		t.Errorf("key %q contains an owner, user or file name", u.Key)
	}

	// Two uploads of the same thing never share a key.
	again, _ := tm.BeginUpload(t.Context(), "member", req)
	u2, _ := tm.store.GetUpload(t.Context(), again.UploadID)
	if u2.Key == u.Key {
		t.Errorf("two uploads got the same key %q", u.Key)
	}
}

func TestBeginUploadNewVersion(t *testing.T) {
	tm := newTestManager(t)
	ctx := t.Context()
	asset := mustCreateAsset(t, tm.store, dam.Asset{Owner: acme, ProjectID: "p1"}, "image/png")

	ticket, err := tm.BeginUpload(ctx, "member", sm.UploadRequest{AssetID: asset.ID, ContentType: "image/jpeg", SizeBytes: 100})
	if err != nil {
		t.Fatalf("BeginUpload: %v", err)
	}
	u, _ := tm.store.GetUpload(ctx, ticket.UploadID)
	if u.Owner != acme || u.ProjectID != "p1" || u.AssetID != asset.ID || !strings.HasPrefix(u.Key, "projects/p1/asset/") {
		t.Errorf("session = %+v; want owner and project taken from the asset", u)
	}

	conflicting := sm.UploadRequest{AssetID: asset.ID, Owner: alice, ContentType: "image/png", SizeBytes: 100}
	if _, err := tm.BeginUpload(ctx, "member", conflicting); !errors.Is(err, dam.ErrInvalidUpload) {
		t.Errorf("version with a different owner = %v, want ErrInvalidUpload", err)
	}
	other := sm.UploadRequest{AssetID: asset.ID, ContentType: "video/mp4", SizeBytes: 100}
	if _, err := tm.BeginUpload(ctx, "member", other); !errors.Is(err, dam.ErrInvalidAsset) {
		t.Errorf("version of another category = %v, want ErrInvalidAsset", err)
	}
	if err := tm.store.MarkAssetDeleted(ctx, asset.ID); err != nil {
		t.Fatalf("MarkAssetDeleted: %v", err)
	}
	if _, err := tm.BeginUpload(ctx, "member", sm.UploadRequest{AssetID: asset.ID, ContentType: "image/png", SizeBytes: 100}); !errors.Is(err, dam.ErrAssetNotFound) {
		t.Errorf("version of a trashed asset = %v, want ErrAssetNotFound", err)
	}
	if _, err := tm.BeginUpload(ctx, "member", sm.UploadRequest{AssetID: uuid.NewString(), ContentType: "image/png", SizeBytes: 100}); !errors.Is(err, dam.ErrAssetNotFound) {
		t.Errorf("version of a missing asset = %v, want ErrAssetNotFound", err)
	}
}

func TestBeginUploadRejects(t *testing.T) {
	tm := newTestManager(t)
	ctx := t.Context()
	with := func(change func(*sm.UploadRequest)) sm.UploadRequest {
		r := newAssetRequest()
		change(&r)
		return r
	}
	tests := []struct {
		name  string
		actor string
		req   sm.UploadRequest
		want  error
	}{
		{"too large", "member", with(func(r *sm.UploadRequest) { r.SizeBytes = sm.DefaultMaxUploadBytes + 1 }), sm.ErrUploadTooLarge},
		{"empty file", "member", with(func(r *sm.UploadRequest) { r.SizeBytes = 0 }), dam.ErrInvalidUpload},
		{"stranger", "stranger", newAssetRequest(), sm.ErrForbidden},
		{"no user", "", newAssetRequest(), sm.ErrForbidden},
		{"someone else's library", "member", with(func(r *sm.UploadRequest) { r.Owner = alice }), sm.ErrForbidden},
		{"project ID with a slash", "member", with(func(r *sm.UploadRequest) { r.ProjectID = "../library" }), dam.ErrInvalidUpload},
		{"blank name", "member", with(func(r *sm.UploadRequest) { r.Name = " " }), dam.ErrInvalidAsset},
		{"no content type", "member", with(func(r *sm.UploadRequest) { r.ContentType = "" }), dam.ErrInvalidUpload},
	}
	tm.authz.AllowProject("member", "../library", sm.AccessWrite) // so the slash case reaches the key check
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tm.BeginUpload(ctx, tt.actor, tt.req); !errors.Is(err, tt.want) {
				t.Errorf("BeginUpload = %v, want %v", err, tt.want)
			}
		})
	}
	if ups := pendingUploads(t, tm.store); len(ups) != 0 {
		t.Errorf("rejected requests left %d sessions", len(ups))
	}

	// A user may always upload to their own library.
	mine := with(func(r *sm.UploadRequest) { r.Owner = alice })
	if _, err := tm.BeginUpload(ctx, "alice", mine); err != nil {
		t.Errorf("alice uploading to her own library: %v", err)
	}
}
