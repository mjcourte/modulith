package manager_test

// Upload session cases for the shared store suite (listed in runStoreTests).

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	sm "gyoza/services/storage/internal/manager"
)

// newAssetUpload is a valid session for a new acme image, expiring in an hour.
func newAssetUpload() sm.Upload {
	return sm.Upload{
		Owner: acme, CreatedBy: "alice",
		Name: "Logo", Tags: []string{"Brand"},
		Bucket: "gyoza", Key: "library/asset/" + uuid.NewString(),
		ContentType: "image/png", SizeBytes: 2048,
		ExpiresAt: time.Now().Add(time.Hour),
	}
}

// versionUpload is a valid session for a new version of asset a.
func versionUpload(a sm.Asset, contentType string) sm.Upload {
	return sm.Upload{
		Owner: a.Owner, CreatedBy: "alice", AssetID: a.ID,
		Bucket: "gyoza", Key: "library/asset/" + uuid.NewString(),
		ContentType: contentType, SizeBytes: 4096,
		ExpiresAt: time.Now().Add(time.Hour),
	}
}

// uploaded is the version the Manager passes once the file for u is in S3
// and recorded in the ledger.
func uploaded(u sm.Upload) sm.Version {
	return sm.Version{ObjectID: u.ObjectID, ContentType: u.ContentType, SizeBytes: u.SizeBytes}
}

func mustCreateUpload(t *testing.T, s sm.Store, u sm.Upload) sm.Upload {
	t.Helper()
	created, err := s.CreateUpload(t.Context(), u)
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}
	return created
}

func testUploadCreateThenGet(t *testing.T, s sm.Store) {
	created := mustCreateUpload(t, s, newAssetUpload())
	if u, err := uuid.Parse(created.ID); err != nil || u.Version() != 7 {
		t.Errorf("ID %q is not a UUIDv7", created.ID)
	}
	if created.CreatedAt.IsZero() || created.CompletedAt != nil || !slices.Equal(created.Tags, []string{"brand"}) {
		t.Errorf("created = %+v; want pending, with tags normalised", created)
	}
	got, err := s.GetUpload(t.Context(), created.ID)
	if err != nil || got.Key != created.Key || got.Name != "Logo" {
		t.Errorf("GetUpload = %+v, %v", got, err)
	}
	if _, err := s.GetUpload(t.Context(), uuid.NewString()); !errors.Is(err, sm.ErrUploadNotFound) {
		t.Errorf("GetUpload of missing session = %v, want ErrUploadNotFound", err)
	}
}

// Everything that can be checked before the file exists is checked at
// CreateUpload, so the browser is never sent to upload a doomed file.
func testUploadCreateRejects(t *testing.T, s sm.Store) {
	ctx := t.Context()
	image := mustCreateAsset(t, s, sm.Asset{Owner: acme}, "image/png")
	alicesImage := mustCreateAsset(t, s, sm.Asset{Owner: alice}, "image/png")
	alicesFolder := mustCreateFolder(t, s, sm.Folder{Owner: alice, Name: "Alice's"})
	trashed := mustCreateAsset(t, s, sm.Asset{Owner: acme}, "image/png")
	if err := s.MarkAssetDeleted(ctx, trashed.ID); err != nil {
		t.Fatalf("MarkAssetDeleted: %v", err)
	}

	tests := []struct {
		name   string
		upload func() sm.Upload
		want   error
	}{
		{"no content type", func() sm.Upload { u := newAssetUpload(); u.ContentType = ""; return u }, sm.ErrInvalidUpload},
		{"blank name", func() sm.Upload { u := newAssetUpload(); u.Name = " "; return u }, sm.ErrInvalidAsset},
		{"other owner's folder", func() sm.Upload { u := newAssetUpload(); u.FolderID = alicesFolder.ID; return u }, sm.ErrFolderNotFound},
		{"version of a missing asset", func() sm.Upload {
			return versionUpload(sm.Asset{ID: uuid.NewString(), Owner: acme}, "image/png")
		}, sm.ErrAssetNotFound},
		{"version of another owner's asset", func() sm.Upload {
			u := versionUpload(alicesImage, "image/png")
			u.Owner = acme
			return u
		}, sm.ErrAssetNotFound},
		{"version of a trashed asset", func() sm.Upload { return versionUpload(trashed, "image/png") }, sm.ErrAssetNotFound},
		{"version changing category", func() sm.Upload { return versionUpload(image, "video/mp4") }, sm.ErrInvalidAsset},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := s.CreateUpload(ctx, tt.upload()); !errors.Is(err, tt.want) {
				t.Errorf("CreateUpload = %v, want %v", err, tt.want)
			}
		})
	}
}

func testCompleteUploadNewAsset(t *testing.T, s sm.Store) {
	ctx := t.Context()
	folder := mustCreateFolder(t, s, sm.Folder{Owner: acme, Name: "Logos"})
	req := newAssetUpload()
	req.FolderID, req.Description = folder.ID, "main logo"
	u := mustCreateUpload(t, s, req)
	file := uploaded(u)

	a, err := s.CompleteUpload(ctx, u.ID, file)
	if err != nil {
		t.Fatalf("CompleteUpload: %v", err)
	}
	if a.Name != "Logo" || a.Description != "main logo" || a.FolderID != folder.ID || a.Owner != acme ||
		a.CreatedBy != "alice" || a.Category != sm.CategoryImage || a.CurrentVersion != 1 || !slices.Equal(a.Tags, []string{"brand"}) {
		t.Errorf("asset = %+v; want the session's details", a)
	}
	if vs := mustListVersions(t, s, a.ID); len(vs) != 1 || vs[0].ObjectID != file.ObjectID || vs[0].CreatedBy != "alice" {
		t.Errorf("versions = %+v; want one, for the uploaded object, created by the session's user", vs)
	}
	done, _ := s.GetUpload(ctx, u.ID)
	if done.CompletedAt == nil || done.ResultAssetID != a.ID || done.ResultVersion != 1 {
		t.Errorf("session after completing = %+v", done)
	}
}

func testCompleteUploadNewVersion(t *testing.T, s sm.Store) {
	ctx := t.Context()
	image := mustCreateAsset(t, s, sm.Asset{Owner: acme}, "image/png")
	u := mustCreateUpload(t, s, versionUpload(image, "image/jpeg"))

	a, err := s.CompleteUpload(ctx, u.ID, uploaded(u))
	if err != nil {
		t.Fatalf("CompleteUpload: %v", err)
	}
	if a.ID != image.ID || a.CurrentVersion != 2 || a.ContentType != "image/jpeg" {
		t.Errorf("asset = %+v; want version 2 (image/jpeg) of %s current", a, image.ID)
	}
	if done, _ := s.GetUpload(ctx, u.ID); done.ResultAssetID != image.ID || done.ResultVersion != 2 {
		t.Errorf("session after completing = %+v", done)
	}
}

// A browser retrying after a timeout must not create a second asset or version.
func testCompleteUploadIsIdempotent(t *testing.T, s sm.Store) {
	ctx := t.Context()
	u := mustCreateUpload(t, s, newAssetUpload())
	file := uploaded(u)
	first, err := s.CompleteUpload(ctx, u.ID, file)
	if err != nil {
		t.Fatalf("CompleteUpload: %v", err)
	}
	second, err := s.CompleteUpload(ctx, u.ID, file)
	if err != nil || second.ID != first.ID {
		t.Errorf("second CompleteUpload = %s, %v; want the same asset %s", second.ID, err, first.ID)
	}
	page, err := s.ListAssets(ctx, sm.AssetQuery{Owner: acme})
	if err != nil || len(page.Assets) != 1 {
		t.Errorf("assets after a repeated complete = %d, %v; want 1", len(page.Assets), err)
	}

	image := mustCreateAsset(t, s, sm.Asset{Owner: alice}, "image/png")
	v := mustCreateUpload(t, s, versionUpload(image, "image/png"))
	for range 2 {
		if _, err := s.CompleteUpload(ctx, v.ID, uploaded(v)); err != nil {
			t.Fatalf("CompleteUpload (version): %v", err)
		}
	}
	if vs := mustListVersions(t, s, image.ID); len(vs) != 2 {
		t.Errorf("versions after a repeated complete = %d, want 2", len(vs))
	}
}

func testCompleteUploadRejects(t *testing.T, s sm.Store) {
	ctx := t.Context()
	if _, err := s.CompleteUpload(ctx, uuid.NewString(), sm.Version{ObjectID: "o", ContentType: "image/png"}); !errors.Is(err, sm.ErrUploadNotFound) {
		t.Errorf("complete a missing session = %v, want ErrUploadNotFound", err)
	}

	u := mustCreateUpload(t, s, newAssetUpload())
	wrong := uploaded(u)
	wrong.SizeBytes++ // not the file the session was signed for
	if _, err := s.CompleteUpload(ctx, u.ID, wrong); !errors.Is(err, sm.ErrInvalidUpload) {
		t.Errorf("complete with a different size = %v, want ErrInvalidUpload", err)
	}

	expiredReq := newAssetUpload()
	expiredReq.ExpiresAt = time.Now().Add(-time.Minute)
	expired := mustCreateUpload(t, s, expiredReq)
	if _, err := s.CompleteUpload(ctx, expired.ID, uploaded(expired)); !errors.Is(err, sm.ErrUploadExpired) {
		t.Errorf("complete an expired session = %v, want ErrUploadExpired", err)
	}

	// Things can change between begin and complete; the checks are repeated.
	image := mustCreateAsset(t, s, sm.Asset{Owner: acme}, "image/png")
	forTrashed := mustCreateUpload(t, s, versionUpload(image, "image/png"))
	if err := s.MarkAssetDeleted(ctx, image.ID); err != nil {
		t.Fatalf("MarkAssetDeleted: %v", err)
	}
	if _, err := s.CompleteUpload(ctx, forTrashed.ID, uploaded(forTrashed)); !errors.Is(err, sm.ErrAssetNotFound) {
		t.Errorf("complete a version of an asset trashed meanwhile = %v, want ErrAssetNotFound", err)
	}

	folder := mustCreateFolder(t, s, sm.Folder{Owner: acme, Name: "Soon gone"})
	intoFolder := newAssetUpload()
	intoFolder.FolderID = folder.ID
	intoTrashed := mustCreateUpload(t, s, intoFolder)
	if _, err := s.DeleteFolder(ctx, folder.ID); err != nil {
		t.Fatalf("DeleteFolder: %v", err)
	}
	if _, err := s.CompleteUpload(ctx, intoTrashed.ID, uploaded(intoTrashed)); !errors.Is(err, sm.ErrFolderNotFound) {
		t.Errorf("complete into a folder trashed meanwhile = %v, want ErrFolderNotFound", err)
	}

	// None of the refusals completed anything.
	for _, id := range []string{u.ID, expired.ID, forTrashed.ID, intoTrashed.ID} {
		if got, _ := s.GetUpload(ctx, id); got.CompletedAt != nil {
			t.Errorf("refused session %s was marked complete", id)
		}
	}
	if page, _ := s.ListAssets(ctx, sm.AssetQuery{Owner: acme}); len(page.Assets) != 0 {
		t.Errorf("refused completions created %d assets", len(page.Assets))
	}
}

func testListExpiredUploads(t *testing.T, s sm.Store) {
	ctx := t.Context()
	at := func(d time.Duration) sm.Upload {
		u := newAssetUpload()
		u.ExpiresAt = time.Now().Add(d)
		return mustCreateUpload(t, s, u)
	}
	older := at(-2 * time.Hour)
	newer := at(-time.Hour)
	at(time.Hour) // still valid
	done := at(time.Minute)
	if _, err := s.CompleteUpload(ctx, done.ID, uploaded(done)); err != nil {
		t.Fatalf("CompleteUpload: %v", err)
	}

	// Asking as if it's two minutes from now: done has expired by then, but
	// it's completed, so it isn't listed.
	got, err := s.ListExpiredUploads(ctx, time.Now().Add(2*time.Minute), 10)
	if err != nil {
		t.Fatalf("ListExpiredUploads: %v", err)
	}
	ids := make([]string, len(got))
	for i, u := range got {
		ids[i] = u.ID
	}
	if want := []string{older.ID, newer.ID}; !slices.Equal(ids, want) {
		t.Errorf("expired = %v, want %v (pending only, soonest-expired first)", ids, want)
	}
	if one, _ := s.ListExpiredUploads(ctx, time.Now(), 1); len(one) != 1 || one[0].ID != older.ID {
		t.Errorf("limit 1 = %+v, want just the oldest", one)
	}
	for _, limit := range []int{0, sm.MaxPageSize + 1} {
		if _, err := s.ListExpiredUploads(ctx, time.Now(), limit); !errors.Is(err, sm.ErrInvalidQuery) {
			t.Errorf("limit %d = %v, want ErrInvalidQuery", limit, err)
		}
	}
}

func testDeleteUpload(t *testing.T, s sm.Store) {
	ctx := t.Context()
	u := mustCreateUpload(t, s, newAssetUpload())
	if err := s.DeleteUpload(ctx, u.ID); err != nil {
		t.Fatalf("DeleteUpload: %v", err)
	}
	if _, err := s.GetUpload(ctx, u.ID); !errors.Is(err, sm.ErrUploadNotFound) {
		t.Errorf("GetUpload after delete = %v, want ErrUploadNotFound", err)
	}
	if err := s.DeleteUpload(ctx, u.ID); err != nil {
		t.Errorf("deleting again = %v, want nil (safe to retry)", err)
	}
}

// The session chooses the ledger object ID its file will be recorded under,
// and CompleteUpload accepts only that object.
func testUploadObjectID(t *testing.T, s sm.Store) {
	ctx := t.Context()
	u := mustCreateUpload(t, s, newAssetUpload())
	other := mustCreateUpload(t, s, newAssetUpload())
	if u.ObjectID == "" || u.ObjectID == u.ID || u.ObjectID == other.ObjectID {
		t.Fatalf("ObjectIDs %q and %q (session %s): want fresh, distinct IDs", u.ObjectID, other.ObjectID, u.ID)
	}
	if got, _ := s.GetUpload(ctx, u.ID); got.ObjectID != u.ObjectID {
		t.Errorf("stored ObjectID = %q, want %q", got.ObjectID, u.ObjectID)
	}

	wrong := uploaded(u)
	wrong.ObjectID = other.ObjectID
	if _, err := s.CompleteUpload(ctx, u.ID, wrong); !errors.Is(err, sm.ErrInvalidUpload) {
		t.Errorf("complete with another session's object = %v, want ErrInvalidUpload", err)
	}
	a, err := s.CompleteUpload(ctx, u.ID, uploaded(u))
	if err != nil {
		t.Fatalf("CompleteUpload: %v", err)
	}
	if vs := mustListVersions(t, s, a.ID); vs[0].ObjectID != u.ObjectID {
		t.Errorf("version object = %q, want the session's %q", vs[0].ObjectID, u.ObjectID)
	}
}

func testUploadSlicesAreCopies(t *testing.T, s sm.Store) {
	req := newAssetUpload()
	req.Tags = []string{"a", "b"}
	u := mustCreateUpload(t, s, req)
	req.Tags[0], u.Tags[0] = "changed-input", "changed-output"
	got, _ := s.GetUpload(t.Context(), u.ID)
	if !slices.Equal(got.Tags, []string{"a", "b"}) {
		t.Errorf("stored tags changed to %q", got.Tags)
	}
}
