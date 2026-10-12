package manager_test

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"gyoza/services/storage/internal/ledger"
	sm "gyoza/services/storage/internal/manager"
)

// uploadAsset runs the whole upload flow as actor and returns the new asset
// and the exact bytes the browser sent.
func uploadAsset(t *testing.T, tm testManager, actor string, req sm.UploadRequest) (sm.Asset, []byte) {
	t.Helper()
	ticket := beginAndPut(t, tm, actor, req)
	a, err := tm.CompleteUpload(t.Context(), actor, ticket.UploadID)
	if err != nil {
		t.Fatalf("CompleteUpload: %v", err)
	}
	return a, fileOf(req.ContentType, int(req.SizeBytes))
}

// download asks for a link as actor and fetches it as the browser would.
func download(t *testing.T, tm testManager, actor, assetID string, version int) (data []byte, filename string) {
	t.Helper()
	d, err := tm.DownloadURL(t.Context(), actor, assetID, version)
	if err != nil {
		t.Fatalf("DownloadURL: %v", err)
	}
	data, filename, err = tm.blobs.GetSigned(d.URL)
	if err != nil {
		t.Fatalf("browser download: %v", err)
	}
	if filename != d.Filename {
		t.Errorf("browser saved as %q, but DownloadURL said %q", filename, d.Filename)
	}
	return data, filename
}

// The whole journey: upload, complete, download; replace with a new version;
// fetch either version; revert and get the old one again.
func TestUploadThenDownload(t *testing.T) {
	tm := newTestManager(t)
	ctx := t.Context()
	a, v1 := uploadAsset(t, tm, "member", newAssetRequest())

	data, name := download(t, tm, "member", a.ID, 0)
	if !bytes.Equal(data, v1) || name != "Logo.png" {
		t.Errorf("download = %d bytes as %q; want the uploaded %d bytes as Logo.png", len(data), name, len(v1))
	}

	ticket := beginAndPut(t, tm, "member", sm.UploadRequest{AssetID: a.ID, ContentType: "image/jpeg", SizeBytes: 3000})
	if _, err := tm.CompleteUpload(ctx, "member", ticket.UploadID); err != nil {
		t.Fatalf("CompleteUpload (version 2): %v", err)
	}
	v2 := fileOf("image/jpeg", 3000)

	if data, name := download(t, tm, "member", a.ID, 0); !bytes.Equal(data, v2) || name != "Logo.jpg" {
		t.Errorf("current = %d bytes as %q; want version 2 as Logo.jpg", len(data), name)
	}
	if data, name := download(t, tm, "member", a.ID, 1); !bytes.Equal(data, v1) || name != "Logo.png" {
		t.Errorf("version 1 = %d bytes as %q; want the original as Logo.png", len(data), name)
	}

	if _, err := tm.store.SetCurrentVersion(ctx, a.ID, 1); err != nil {
		t.Fatalf("SetCurrentVersion: %v", err)
	}
	if data, _ := download(t, tm, "member", a.ID, 0); !bytes.Equal(data, v1) {
		t.Error("after reverting, the current download isn't version 1")
	}
}

func TestDownloadURLExpires(t *testing.T) {
	tm := newTestManagerWith(t, sm.Config{Bucket: testBucket, DownloadURLTTL: 50 * time.Millisecond}, nil)
	a, _ := uploadAsset(t, tm, "member", newAssetRequest())
	d, err := tm.DownloadURL(t.Context(), "member", a.ID, 0)
	if err != nil {
		t.Fatalf("DownloadURL: %v", err)
	}
	if d.ExpiresAt.After(time.Now().Add(time.Second)) {
		t.Errorf("ExpiresAt = %v, want within the configured 50ms", d.ExpiresAt)
	}
	time.Sleep(100 * time.Millisecond)
	if _, _, err := tm.blobs.GetSigned(d.URL); err == nil {
		t.Error("an expired download link still worked")
	}
}

func TestDownloadPermissions(t *testing.T) {
	tm := newTestManager(t)
	ctx := t.Context()
	tm.authz.AllowOwner("viewer", acme, sm.AccessRead)
	tm.authz.AllowProject("member", "p1", sm.AccessWrite)
	tm.authz.AllowProject("guest", "p1", sm.AccessRead)

	req := newAssetRequest()
	req.ProjectID = "p1"
	a, _ := uploadAsset(t, tm, "member", req)

	for _, actor := range []string{"member", "viewer", "guest"} {
		if _, err := tm.DownloadURL(ctx, actor, a.ID, 0); err != nil {
			t.Errorf("%s downloading: %v, want allowed", actor, err)
		}
	}
	for _, actor := range []string{"stranger", ""} {
		if _, err := tm.DownloadURL(ctx, actor, a.ID, 0); !errors.Is(err, sm.ErrForbidden) {
			t.Errorf("%q downloading = %v, want ErrForbidden", actor, err)
		}
	}
	// Read access doesn't extend to uploading.
	if _, err := tm.BeginUpload(ctx, "viewer", newAssetRequest()); !errors.Is(err, sm.ErrForbidden) {
		t.Errorf("viewer uploading = %v, want ErrForbidden", err)
	}
	// A stranger can't learn which versions exist.
	if _, err := tm.DownloadURL(ctx, "stranger", a.ID, 99); !errors.Is(err, sm.ErrForbidden) {
		t.Errorf("stranger asking for version 99 = %v, want ErrForbidden, not ErrVersionNotFound", err)
	}
}

func TestDownloadRejects(t *testing.T) {
	tm := newTestManager(t)
	ctx := t.Context()
	a, _ := uploadAsset(t, tm, "member", newAssetRequest())

	if _, err := tm.DownloadURL(ctx, "member", uuid.NewString(), 0); !errors.Is(err, sm.ErrAssetNotFound) {
		t.Errorf("missing asset = %v, want ErrAssetNotFound", err)
	}
	for _, v := range []int{-1, 2} {
		if _, err := tm.DownloadURL(ctx, "member", a.ID, v); !errors.Is(err, sm.ErrVersionNotFound) {
			t.Errorf("version %d = %v, want ErrVersionNotFound", v, err)
		}
	}

	// The asset says version 1 lives in a ledger object that's been deleted.
	vs := mustListVersions(t, tm.store, a.ID)
	if err := tm.ledger.MarkDeleted(ctx, vs[0].ObjectID); err != nil {
		t.Fatalf("ledger MarkDeleted: %v", err)
	}
	if _, err := tm.DownloadURL(ctx, "member", a.ID, 0); !errors.Is(err, sm.ErrAssetNotFound) {
		t.Errorf("version whose ledger object is gone = %v, want ErrAssetNotFound", err)
	}

	// Trashed assets must be restored before downloading.
	b, _ := uploadAsset(t, tm, "member", newAssetRequest())
	if err := tm.store.MarkAssetDeleted(ctx, b.ID); err != nil {
		t.Fatalf("MarkAssetDeleted: %v", err)
	}
	if _, err := tm.DownloadURL(ctx, "member", b.ID, 0); !errors.Is(err, sm.ErrAssetNotFound) {
		t.Errorf("trashed asset = %v, want ErrAssetNotFound", err)
	}
}

// putObject stores bytes in the blobs and records them in the ledger as kind,
// the way another service (e.g. the document manager writing an export) would.
func putObject(t *testing.T, tm testManager, kind ledger.Kind, contentType string, data []byte) ledger.Object {
	t.Helper()
	ctx := t.Context()
	key := "projects/p1/" + string(kind) + "/" + uuid.NewString()
	url, err := tm.blobs.PresignPut(ctx, testBucket, key, contentType, int64(len(data)), time.Minute)
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}
	if err := tm.blobs.PutSigned(url, contentType, data); err != nil {
		t.Fatalf("PutSigned: %v", err)
	}
	obj, err := tm.ledger.Create(ctx, ledger.Object{
		Owner: acme, ProjectID: "p1", Kind: kind, Bucket: testBucket, Key: key,
		SizeBytes: int64(len(data)), ContentType: contentType,
	})
	if err != nil {
		t.Fatalf("ledger Create: %v", err)
	}
	return obj
}

func TestExportURL(t *testing.T) {
	tm := newTestManager(t)
	ctx := t.Context()
	tm.authz.AllowProject("guest", "p1", sm.AccessRead)
	video := []byte("pretend this is an mp4")
	export := putObject(t, tm, ledger.KindExport, "video/mp4", video)

	d, err := tm.ExportURL(ctx, "guest", export.ID)
	if err != nil {
		t.Fatalf("ExportURL: %v", err)
	}
	data, name, err := tm.blobs.GetSigned(d.URL)
	wantName := "export-" + export.CreatedAt.Format("2006-01-02") + ".mp4"
	if err != nil || !bytes.Equal(data, video) || name != wantName {
		t.Errorf("export download = %q as %q, %v; want the export as %q", data, name, err, wantName)
	}

	if _, err := tm.ExportURL(ctx, "stranger", export.ID); !errors.Is(err, sm.ErrForbidden) {
		t.Errorf("stranger = %v, want ErrForbidden", err)
	}
	// ExportURL serves exports only, not any object it's given an ID for.
	snapshot := putObject(t, tm, ledger.KindSnapshot, "application/octet-stream", []byte("doc"))
	asset := putObject(t, tm, ledger.KindAsset, "image/png", fileOf("image/png", 64))
	for name, id := range map[string]string{"snapshot": snapshot.ID, "asset object": asset.ID, "missing": "not-an-object"} {
		if _, err := tm.ExportURL(ctx, "member", id); !errors.Is(err, sm.ErrExportNotFound) {
			t.Errorf("%s = %v, want ErrExportNotFound", name, err)
		}
	}
	if err := tm.ledger.MarkDeleted(ctx, export.ID); err != nil {
		t.Fatalf("ledger MarkDeleted: %v", err)
	}
	if _, err := tm.ExportURL(ctx, "guest", export.ID); !errors.Is(err, sm.ErrExportNotFound) {
		t.Errorf("deleted export = %v, want ErrExportNotFound", err)
	}
}
