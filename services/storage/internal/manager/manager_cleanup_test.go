package manager_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"gyoza/services/storage/internal/blob"
	"gyoza/services/storage/internal/dam"
	"gyoza/services/storage/internal/ledger"
	sm "gyoza/services/storage/internal/manager"
)

// week is past the default trash retention (7 days) by a comfortable margin.
const week = 8 * 24 * time.Hour

// fileExists reports whether the blob store still holds bucket/key.
func fileExists(t *testing.T, tm testManager, bucket, key string) bool {
	t.Helper()
	_, err := tm.blobs.Stat(t.Context(), bucket, key)
	if err != nil && !errors.Is(err, blob.ErrNotFound) {
		t.Fatalf("Stat: %v", err)
	}
	return err == nil
}

// objectExists reports whether the ledger still has the object, deleted or not.
func objectExists(t *testing.T, tm testManager, id string) bool {
	t.Helper()
	_, err := tm.ledger.Get(t.Context(), id)
	if err != nil && !errors.Is(err, ledger.ErrNotFound) {
		t.Fatalf("ledger Get: %v", err)
	}
	return err == nil
}

func mustCleanup(t *testing.T, tm testManager, now time.Time) sm.CleanupReport {
	t.Helper()
	r, err := tm.Cleanup(t.Context(), now)
	if err != nil {
		t.Fatalf("Cleanup: %v (did %+v)", err, r)
	}
	return r
}

// Uploads that expired before completing are removed with their files, and
// with the ledger object of a completion that got that far.
func TestCleanupExpiredUploads(t *testing.T) {
	tm := newTestManager(t)
	ctx := t.Context()
	abandoned := beginAndPut(t, tm, "member", newAssetRequest())
	halfDone := beginAndPut(t, tm, "member", newAssetRequest())
	u, _ := tm.store.GetUpload(ctx, halfDone.UploadID)
	// As if CompleteUpload recorded the file in the ledger, then crashed.
	if _, err := tm.ledger.Create(ctx, ledger.Object{ID: u.ObjectID, Owner: acme, Kind: ledger.KindAsset,
		Bucket: u.Bucket, Key: u.Key, SizeBytes: u.SizeBytes, ContentType: u.ContentType}); err != nil {
		t.Fatalf("ledger Create: %v", err)
	}
	expired := u.ExpiresAt

	// Just expired: left alone for the clock-skew margin, in case another
	// replica's clock still lets a completion through.
	if r := mustCleanup(t, tm, expired.Add(time.Minute)); r.ExpiredUploads != 0 {
		t.Fatalf("right after expiry: removed %d uploads, want 0", r.ExpiredUploads)
	}
	r := mustCleanup(t, tm, expired.Add(sm.ClockSkew+time.Minute))
	if r.ExpiredUploads != 2 {
		t.Errorf("removed %d expired uploads, want 2", r.ExpiredUploads)
	}
	for _, id := range []string{abandoned.UploadID, halfDone.UploadID} {
		if _, err := tm.store.GetUpload(ctx, id); !errors.Is(err, dam.ErrUploadNotFound) {
			t.Errorf("session %s after cleanup = %v, want ErrUploadNotFound", id, err)
		}
	}
	if fileExists(t, tm, u.Bucket, u.Key) {
		t.Error("the expired upload's file is still in storage")
	}
	if objectExists(t, tm, u.ObjectID) {
		t.Error("the expired upload's ledger object is still there")
	}
}

// Trash past its retention goes for good: every version's file and ledger
// object, the asset, its folder, and the old upload sessions.
func TestCleanupPurgesTrash(t *testing.T) {
	tm := newTestManager(t)
	ctx := t.Context()
	folder, err := tm.CreateFolder(ctx, "member", dam.Folder{Owner: acme, Name: "Old"})
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	req := newAssetRequest()
	req.FolderID = folder.ID
	a, _ := uploadAsset(t, tm, "member", req)
	uploadAsset(t, tm, "member", sm.UploadRequest{AssetID: a.ID, ContentType: "image/png", SizeBytes: 2048})
	kept, _ := uploadAsset(t, tm, "member", newAssetRequest())
	versions := mustListVersions(t, tm.store, a.ID)
	keptVersion := mustListVersions(t, tm.store, kept.ID)[0]
	objects := map[string]ledger.Object{}
	for _, v := range append(versions, keptVersion) {
		obj, err := tm.ledger.Get(ctx, v.ObjectID)
		if err != nil {
			t.Fatalf("ledger Get: %v", err)
		}
		objects[v.ObjectID] = obj
	}
	if _, err := tm.DeleteFolderTree(ctx, "member", folder.ID); err != nil {
		t.Fatalf("DeleteFolderTree: %v", err)
	}
	now := time.Now()

	if r := mustCleanup(t, tm, now.Add(6*24*time.Hour)); r.Assets != 0 || r.Folders != 0 {
		t.Fatalf("after 6 days: %+v, want nothing purged yet", r)
	}
	r := mustCleanup(t, tm, now.Add(week))
	if want := (sm.CleanupReport{CompletedUploads: 3, Assets: 1, Folders: 1}); r != want {
		t.Errorf("after a week: %+v, want %+v", r, want)
	}
	if _, err := tm.store.GetAsset(ctx, a.ID); !errors.Is(err, dam.ErrAssetNotFound) {
		t.Errorf("purged asset: %v, want ErrAssetNotFound", err)
	}
	if _, err := tm.store.GetFolder(ctx, folder.ID); !errors.Is(err, dam.ErrFolderNotFound) {
		t.Errorf("purged folder: %v, want ErrFolderNotFound", err)
	}
	for _, v := range versions {
		obj := objects[v.ObjectID]
		if fileExists(t, tm, obj.Bucket, obj.Key) || objectExists(t, tm, obj.ID) {
			t.Errorf("version %d's file or ledger object survived the purge", v.Number)
		}
	}
	keptObj := objects[keptVersion.ObjectID]
	if !fileExists(t, tm, keptObj.Bucket, keptObj.Key) || !objectExists(t, tm, keptObj.ID) {
		t.Error("the live asset's file or ledger object was purged")
	}
	if again := mustCleanup(t, tm, now.Add(week)); again != (sm.CleanupReport{}) {
		t.Errorf("running it again: %+v, want nothing left to do", again)
	}
}

// Ledger objects deleted outside the DAM, e.g. a deleted project's exports,
// go with their files once the retention period is over.
func TestCleanupPurgesDeletedLedgerObjects(t *testing.T) {
	tm := newTestManager(t)
	ctx := t.Context()
	export, err := tm.ledger.Create(ctx, ledger.Object{Owner: project, Kind: ledger.KindExport,
		Bucket: testBucket, Key: "projects/p1/export/x", SizeBytes: 3, ContentType: "video/mp4"})
	if err != nil {
		t.Fatalf("ledger Create: %v", err)
	}
	url, _ := tm.blobs.PresignPut(ctx, export.Bucket, export.Key, export.ContentType, 3, time.Minute)
	if err := tm.blobs.PutSigned(url, export.ContentType, []byte("mp4")); err != nil {
		t.Fatalf("put export: %v", err)
	}
	if _, err := tm.DeleteOwned(ctx, project); err != nil {
		t.Fatalf("DeleteOwned: %v", err)
	}

	if r := mustCleanup(t, tm, time.Now().Add(time.Hour)); r.Objects != 0 {
		t.Fatalf("an hour later: purged %d objects, want 0", r.Objects)
	}
	if r := mustCleanup(t, tm, time.Now().Add(week)); r.Objects != 1 {
		t.Errorf("after a week: purged %d objects, want 1", r.Objects)
	}
	if fileExists(t, tm, export.Bucket, export.Key) || objectExists(t, tm, export.ID) {
		t.Error("the deleted export's file or ledger object survived")
	}
}

// Once the cleanup job may purge something, it can't be restored any more,
// so a restore can't race the deletion of its files.
func TestRestoreEndsWithRetention(t *testing.T) {
	tm := newTestManager(t)
	ctx := t.Context()
	a, _ := uploadAsset(t, tm, "member", newAssetRequest())
	folder, _ := tm.CreateFolder(ctx, "member", dam.Folder{Owner: acme, Name: "F"})
	if err := tm.DeleteAsset(ctx, "member", a.ID); err != nil {
		t.Fatalf("DeleteAsset: %v", err)
	}
	if _, err := tm.DeleteFolder(ctx, "member", folder.ID); err != nil {
		t.Fatalf("DeleteFolder: %v", err)
	}
	trashed := time.Now()
	lastChance := trashed.Add(sm.DefaultTrashRetention - sm.ClockSkew - time.Minute)
	tooLate := trashed.Add(sm.DefaultTrashRetention - sm.ClockSkew + time.Minute)

	sm.SetClock(tm.Manager, func() time.Time { return tooLate })
	if _, err := tm.RestoreAsset(ctx, "member", a.ID); !errors.Is(err, dam.ErrAssetNotFound) {
		t.Errorf("RestoreAsset too late = %v, want ErrAssetNotFound", err)
	}
	if _, err := tm.RestoreFolder(ctx, "member", folder.ID); !errors.Is(err, dam.ErrFolderNotFound) {
		t.Errorf("RestoreFolder too late = %v, want ErrFolderNotFound", err)
	}

	sm.SetClock(tm.Manager, func() time.Time { return lastChance })
	if _, err := tm.RestoreAsset(ctx, "member", a.ID); err != nil {
		t.Errorf("RestoreAsset in time = %v", err)
	}
	if _, err := tm.RestoreFolder(ctx, "member", folder.ID); err != nil {
		t.Errorf("RestoreFolder in time = %v", err)
	}
}

// failingDeletes makes the first n blob deletions fail, as a flaky S3 might.
type failingDeletes struct {
	*blob.Memory
	n int
}

func (f *failingDeletes) Delete(ctx context.Context, bucket, key string) error {
	if f.n > 0 {
		f.n--
		return errors.New("S3 unavailable")
	}
	return f.Memory.Delete(ctx, bucket, key)
}

// A run that fails part-way reports what it did and leaves the rest for the
// next run, which finishes the job without redoing or skipping anything.
func TestCleanupResumesAfterFailure(t *testing.T) {
	flaky := &failingDeletes{}
	tm := newTestManagerWith(t, sm.Config{Bucket: testBucket}, func(b *blob.Memory) blob.Store {
		flaky.Memory = b
		return flaky
	})
	ctx := t.Context()
	a, _ := uploadAsset(t, tm, "member", newAssetRequest())
	b, _ := uploadAsset(t, tm, "member", newAssetRequest())
	for _, id := range []string{a.ID, b.ID} {
		if err := tm.DeleteAsset(ctx, "member", id); err != nil {
			t.Fatalf("DeleteAsset: %v", err)
		}
	}
	later := time.Now().Add(week)

	flaky.n = 1 // the first asset's file can't be deleted this time
	r, err := tm.Cleanup(ctx, later)
	if err == nil || r.Assets != 0 {
		t.Fatalf("Cleanup with failing S3 = %+v, %v; want an error and nothing purged", r, err)
	}
	if r := mustCleanup(t, tm, later); r.Assets != 2 {
		t.Errorf("next run purged %d assets, want 2", r.Assets)
	}
	if page, _ := tm.store.ListAssets(ctx, dam.AssetQuery{Owner: acme, IncludeDeleted: true}); len(page.Assets) != 0 {
		t.Errorf("%d assets left after the second run", len(page.Assets))
	}
}

// RunCleanup runs straight away, then stops when its context is cancelled.
func TestRunCleanupStops(t *testing.T) {
	tm := newTestManager(t)
	ticket := beginAndPut(t, tm, "member", newAssetRequest())
	u, _ := tm.store.GetUpload(t.Context(), ticket.UploadID)
	sm.SetClock(tm.Manager, func() time.Time { return u.ExpiresAt.Add(time.Hour) })

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		tm.RunCleanup(ctx, time.Hour)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := tm.store.GetUpload(t.Context(), u.ID); errors.Is(err, dam.ErrUploadNotFound) {
			break // the first run happened
		}
		if time.Now().After(deadline) {
			t.Fatal("RunCleanup didn't run straight away")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunCleanup didn't stop after cancel")
	}
}

// A run keeps going until everything due is done, however many batches.
func TestCleanupDrainsEveryBatch(t *testing.T) {
	tm := newTestManager(t)
	n := sm.CleanupBatch + 1
	for range n {
		if _, err := tm.BeginUpload(t.Context(), "member", newAssetRequest()); err != nil {
			t.Fatalf("BeginUpload: %v", err)
		}
	}
	if r := mustCleanup(t, tm, time.Now().Add(time.Hour+sm.ClockSkew)); r.ExpiredUploads != n {
		t.Errorf("removed %d expired uploads, want all %d", r.ExpiredUploads, n)
	}
}
