package manager_test

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"gyoza/services/storage/internal/dam"
	sm "gyoza/services/storage/internal/manager"
)

// resolveFixture: acme's asset (two versions) used by project p1 at version 1,
// and an unused acme asset. "viewer" reads p1 only; "member" writes acme and p1.
type resolveFixture struct {
	tm             testManager
	used, unused   dam.Asset
	v1Data, v2Data []byte
	ref            dam.AssetRef
}

func newResolveFixture(t *testing.T) resolveFixture {
	t.Helper()
	tm := newTestManager(t)
	tm.authz.AllowProject("member", "p1", sm.AccessWrite)
	tm.authz.AllowProject("viewer", "p1", sm.AccessRead)
	tm.authz.SetProjectOwner("p1", acme)
	used, v1 := uploadAsset(t, tm, "member", newAssetRequest())
	next := sm.UploadRequest{AssetID: used.ID, ContentType: "image/png", SizeBytes: 4096}
	if _, err := tm.CompleteUpload(t.Context(), "member", beginAndPut(t, tm, "member", next).UploadID); err != nil {
		t.Fatalf("second version: %v", err)
	}
	unused, _ := uploadAsset(t, tm, "member", newAssetRequest())
	ref := dam.AssetRef{AssetID: used.ID, Version: 1}
	if err := tm.RecordAssetUse(t.Context(), "member", "p1", ref); err != nil {
		t.Fatalf("RecordAssetUse: %v", err)
	}
	return resolveFixture{tm: tm, used: used, unused: unused, v1Data: v1, v2Data: fileOf("image/png", 4096), ref: ref}
}

// mustResolve resolves ref as actor in p1 and downloads the bytes.
func (f resolveFixture) mustResolve(t *testing.T, actor string, ref dam.AssetRef) (sm.ResolvedRef, []byte) {
	t.Helper()
	r, err := f.tm.ResolveRef(t.Context(), actor, "p1", ref)
	if err != nil {
		t.Fatalf("ResolveRef(%s, %s): %v", actor, ref, err)
	}
	data, _, err := f.tm.blobs.GetSigned(r.Download.URL)
	if err != nil {
		t.Fatalf("download %s: %v", ref, err)
	}
	return r, data
}

// A project's readers read what it uses, at the pinned version, even with no
// access to the asset's owner.
func TestResolveRefThroughProject(t *testing.T) {
	f := newResolveFixture(t)
	r, data := f.mustResolve(t, "viewer", f.ref)
	if r.Asset.ID != f.used.ID || r.Version.Number != 1 || r.Asset.CurrentVersion != 2 {
		t.Errorf("resolved %s@%d (current %d), want %s@1 (current 2)", r.Asset.ID, r.Version.Number, r.Asset.CurrentVersion, f.used.ID)
	}
	if !bytes.Equal(data, f.v1Data) {
		t.Error("downloaded bytes aren't version 1's: a pinned ref must not follow new uploads")
	}
	if r.Download.Filename != "Logo.png" {
		t.Errorf("Filename = %q, want Logo.png", r.Download.Filename)
	}

	// Only the pinned version is granted, and only what p1 uses.
	if _, err := f.tm.ResolveRef(t.Context(), "viewer", "p1", dam.AssetRef{AssetID: f.used.ID, Version: 2}); !errors.Is(err, sm.ErrForbidden) {
		t.Errorf("an unused version of a used asset = %v, want ErrForbidden", err)
	}
	if _, err := f.tm.ResolveRef(t.Context(), "viewer", "p1", dam.AssetRef{AssetID: f.unused.ID, Version: 1}); !errors.Is(err, sm.ErrForbidden) {
		t.Errorf("an asset p1 doesn't use = %v, want ErrForbidden", err)
	}
	// Direct access needs no use: the member reads acme's files anyway.
	if _, data := f.mustResolve(t, "member", dam.AssetRef{AssetID: f.used.ID, Version: 2}); !bytes.Equal(data, f.v2Data) {
		t.Error("direct read of version 2 got the wrong bytes")
	}
}

func TestResolveRefRejects(t *testing.T) {
	f := newResolveFixture(t)
	ctx := t.Context()
	f.tm.authz.AllowProject("viewer", "p2", sm.AccessRead)
	tests := []struct {
		name, actor, project string
		ref                  dam.AssetRef
		want                 error
	}{
		{"stranger", "stranger", "p1", f.ref, sm.ErrForbidden},
		{"no actor", "", "p1", f.ref, sm.ErrForbidden},
		{"project the actor can't read", "viewer", "p9", f.ref, sm.ErrForbidden},
		{"another project that doesn't use it", "viewer", "p2", f.ref, sm.ErrForbidden},
		{"stranger asks about a missing asset", "stranger", "p1", dam.AssetRef{AssetID: "0198f000-0000-7000-8000-000000000000", Version: 1}, sm.ErrForbidden},
		{"missing asset", "viewer", "p1", dam.AssetRef{AssetID: "0198f000-0000-7000-8000-000000000000", Version: 1}, dam.ErrAssetNotFound},
		{"missing version, direct", "member", "p1", dam.AssetRef{AssetID: f.used.ID, Version: 3}, dam.ErrVersionNotFound},
		{"version 0", "viewer", "p1", dam.AssetRef{AssetID: f.used.ID}, dam.ErrInvalidQuery},
		{"no project", "viewer", "", f.ref, dam.ErrInvalidQuery},
	}
	for _, tt := range tests {
		if _, err := f.tm.ResolveRef(ctx, tt.actor, tt.project, tt.ref); !errors.Is(err, tt.want) {
			t.Errorf("%s: ResolveRef = %v, want %v", tt.name, err, tt.want)
		}
	}
}

// A used asset keeps resolving from the trash until the purge; the trash
// listing says how many projects use it.
func TestResolveRefTrashedAndPurged(t *testing.T) {
	f := newResolveFixture(t)
	ctx := t.Context()
	if err := f.tm.RecordAssetUse(ctx, "member", "p1", dam.AssetRef{AssetID: f.used.ID, Version: 2}); err != nil {
		t.Fatalf("RecordAssetUse: %v", err)
	}
	f.tm.authz.AllowProject("member", "p2", sm.AccessWrite)
	f.tm.authz.SetProjectOwner("p2", acme)
	if err := f.tm.RecordAssetUse(ctx, "member", "p2", f.ref); err != nil {
		t.Fatalf("RecordAssetUse: %v", err)
	}
	if err := f.tm.DeleteAsset(ctx, "member", f.used.ID); err != nil {
		t.Fatalf("DeleteAsset: %v", err)
	}
	if err := f.tm.DeleteAsset(ctx, "member", f.unused.ID); err != nil {
		t.Fatalf("DeleteAsset: %v", err)
	}

	r, data := f.mustResolve(t, "viewer", f.ref)
	if r.Asset.DeletedAt == nil || !bytes.Equal(data, f.v1Data) {
		t.Errorf("trashed but used: DeletedAt %v, bytes match %v; want marked trashed and readable", r.Asset.DeletedAt, bytes.Equal(data, f.v1Data))
	}
	// Direct access serves live assets only, as DownloadURL does.
	if _, err := f.tm.ResolveRef(ctx, "member", "p1", dam.AssetRef{AssetID: f.unused.ID, Version: 1}); !errors.Is(err, dam.ErrAssetNotFound) {
		t.Errorf("trashed, unused, direct = %v, want ErrAssetNotFound", err)
	}

	trash, err := f.tm.ListTrash(ctx, "member", acme, "")
	if err != nil {
		t.Fatalf("ListTrash: %v", err)
	}
	if trash.Uses[f.used.ID] != 2 || trash.Uses[f.unused.ID] != 0 {
		t.Errorf("trash uses = %v, want %s: 2 (p1, p2), %s: 0", trash.Uses, f.used.ID, f.unused.ID)
	}

	// After the purge the ref resolves to nothing.
	if _, err := f.tm.Cleanup(ctx, time.Now().Add(sm.DefaultTrashRetention+time.Hour)); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if _, err := f.tm.ResolveRef(ctx, "viewer", "p1", f.ref); !errors.Is(err, dam.ErrAssetNotFound) {
		t.Errorf("purged = %v, want ErrAssetNotFound", err)
	}
}
