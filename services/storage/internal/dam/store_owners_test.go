package dam_test

// Owner lifecycle cases for the shared store suite (listed in runStoreTests).

import (
	"errors"
	"slices"
	"testing"
	"time"

	"gyoza/services/storage/internal/dam"
	"gyoza/services/storage/internal/ledger"
)

func testTransferOwner(t *testing.T, s dam.Store) {
	ctx := t.Context()
	// alice's library: Brand > Logos with an asset, a top-level asset, a trashed asset.
	brand := mustCreateFolder(t, s, dam.Folder{Owner: alice, Name: "Brand", CreatedBy: "alice"})
	logos := mustCreateFolder(t, s, dam.Folder{Owner: alice, Name: "Logos", ParentID: brand.ID})
	inLogos := mustCreateAsset(t, s, dam.Asset{Owner: alice, FolderID: logos.ID}, "image/png")
	top := mustCreateAsset(t, s, dam.Asset{Owner: alice}, "image/png")
	trashed := mustCreateAsset(t, s, dam.Asset{Owner: alice}, "image/png")
	if err := s.MarkAssetDeleted(ctx, trashed.ID); err != nil {
		t.Fatalf("MarkAssetDeleted: %v", err)
	}
	mustCreateFolder(t, s, dam.Folder{Owner: acme, Name: "brand"}) // acme already has a "brand"
	// ...and a top-level "logos": alice's Logos sits inside Brand, so it must not be renamed.
	mustCreateFolder(t, s, dam.Folder{Owner: acme, Name: "logos"})

	got, err := s.TransferOwner(ctx, alice, acme)
	if err != nil || got.Assets != 3 || got.Folders != 2 || got.Renamed != 1 {
		t.Fatalf("TransferOwner = %+v, %v; want 3 assets, 2 folders, 1 renamed", got, err)
	}
	if f := mustGetFolder(t, s, brand.ID); f.Owner != acme || f.Name != "Brand (2)" {
		t.Errorf("Brand = %+v; want acme's, renamed to avoid acme's \"brand\"", f)
	}
	if f := mustGetFolder(t, s, logos.ID); f.Owner != acme || f.ParentID != brand.ID || f.Name != "Logos" {
		t.Errorf("Logos = %+v; want acme's, still inside Brand", f)
	}
	for _, a := range []dam.Asset{inLogos, top, trashed} {
		if g := mustGetAsset(t, s, a.ID); g.Owner != acme || g.FolderID != a.FolderID {
			t.Errorf("asset %s = %+v; want acme's, same folder", a.ID, g)
		}
	}
	if g := mustGetAsset(t, s, trashed.ID); g.DeletedAt == nil {
		t.Error("a trashed asset was restored by the transfer")
	}
	if page, _ := s.ListAssets(ctx, dam.AssetQuery{Owner: alice, IncludeDeleted: true}); len(page.Assets) != 0 {
		t.Errorf("alice still owns %d assets", len(page.Assets))
	}

	invalid := map[string][2]ledger.Owner{
		"to a project":   {acme, project},
		"from a project": {project, acme},
		"to the same":    {acme, acme},
	}
	for name, pair := range invalid {
		if _, err := s.TransferOwner(ctx, pair[0], pair[1]); !errors.Is(err, dam.ErrInvalidQuery) {
			t.Errorf("%s = %v, want ErrInvalidQuery", name, err)
		}
	}
}

// The upload session's check is repeated at completion, so ownership moving
// mid-upload stops it: the session was for alice's asset, which is now acme's.
func testTransferOwnerMidUpload(t *testing.T, s dam.Store) {
	ctx := t.Context()
	a := mustCreateAsset(t, s, dam.Asset{Owner: alice}, "image/png")
	u := mustCreateUpload(t, s, versionUpload(a, "image/png"))
	if _, err := s.TransferOwner(ctx, alice, acme); err != nil {
		t.Fatalf("TransferOwner: %v", err)
	}
	if _, err := s.CompleteUpload(ctx, u.ID, uploaded(u)); !errors.Is(err, dam.ErrAssetNotFound) {
		t.Errorf("CompleteUpload after the owner changed = %v, want ErrAssetNotFound", err)
	}
	if vs := mustListVersions(t, s, a.ID); len(vs) != 1 {
		t.Errorf("versions = %d, want 1", len(vs))
	}
}

func testMarkDeletedByOwner(t *testing.T, s dam.Store) {
	ctx := t.Context()
	f := mustCreateFolder(t, s, dam.Folder{Owner: project, Name: "Scenes"})
	inF := mustCreateAsset(t, s, dam.Asset{Owner: project, FolderID: f.ID}, "image/png")
	mustCreateAsset(t, s, dam.Asset{Owner: project}, "image/png")
	associated := mustCreateAsset(t, s, dam.Asset{Owner: acme, ProjectID: "p1"}, "image/png")

	del, err := s.MarkDeletedByOwner(ctx, project)
	if err != nil || del.Folders != 1 || del.Assets != 2 || del.DeletionID == "" {
		t.Fatalf("MarkDeletedByOwner = %+v, %v", del, err)
	}
	if g := mustGetAsset(t, s, associated.ID); g.DeletedAt != nil {
		t.Error("an asset merely associated with the project was trashed")
	}
	// All of it comes back together.
	if r, err := s.RestoreDeletion(ctx, del.DeletionID); err != nil || r.Folders != 1 || r.Assets != 2 {
		t.Errorf("RestoreDeletion = %+v, %v", r, err)
	}
	if g := mustGetAsset(t, s, inF.ID); g.DeletedAt != nil || g.FolderID != f.ID {
		t.Errorf("restored asset = %+v; want live in its folder", g)
	}
	if again, err := s.MarkDeletedByOwner(ctx, ledger.Owner{}); !errors.Is(err, dam.ErrInvalidQuery) {
		t.Errorf("no owner = %+v, %v; want ErrInvalidQuery", again, err)
	}
}

func testClearCreatedByStore(t *testing.T, s dam.Store) {
	ctx := t.Context()
	a := mustCreateAsset(t, s, dam.Asset{Owner: acme, CreatedBy: "alice"}, "image/png") // version 1 by alice too
	f := mustCreateFolder(t, s, dam.Folder{Owner: acme, Name: "Hers", CreatedBy: "alice"})
	bobsVersion := version("image/png")
	bobsVersion.CreatedBy = "bob" // the shared helper makes versions by alice
	bobs, err := s.CreateAsset(ctx, dam.Asset{Owner: acme, Name: "Bob's", CreatedBy: "bob"}, bobsVersion)
	if err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}

	n, err := s.ClearCreatedBy(ctx, "alice")
	if err != nil || n != 3 {
		t.Fatalf("ClearCreatedBy = %d, %v; want 3 (asset, its version, folder)", n, err)
	}
	if g := mustGetAsset(t, s, a.ID); g.CreatedBy != "" {
		t.Errorf("asset creator = %q", g.CreatedBy)
	}
	if vs := mustListVersions(t, s, a.ID); vs[0].CreatedBy != "" {
		t.Errorf("version creator = %q", vs[0].CreatedBy)
	}
	if g := mustGetFolder(t, s, f.ID); g.CreatedBy != "" {
		t.Errorf("folder creator = %q", g.CreatedBy)
	}
	if g := mustGetAsset(t, s, bobs.ID); g.CreatedBy != "bob" {
		t.Error("bob's asset lost its creator")
	}
	if vs := mustListVersions(t, s, bobs.ID); vs[0].CreatedBy != "bob" {
		t.Error("bob's version lost its creator")
	}
	if _, err := s.ClearCreatedBy(ctx, ""); !errors.Is(err, dam.ErrInvalidQuery) {
		t.Errorf("empty user = %v, want ErrInvalidQuery", err)
	}
}

func testExpireUploadsBy(t *testing.T, s dam.Store) {
	ctx := t.Context()
	mine := newAssetUpload() // CreatedBy alice
	pending := mustCreateUpload(t, s, mine)
	done := mustCreateUpload(t, s, mine)
	if _, err := s.CompleteUpload(ctx, done.ID, uploaded(done)); err != nil {
		t.Fatalf("CompleteUpload: %v", err)
	}
	bobs := newAssetUpload()
	bobs.CreatedBy = "bob"
	bob := mustCreateUpload(t, s, bobs)

	n, err := s.ExpireUploadsBy(ctx, "alice")
	if err != nil || n != 1 {
		t.Fatalf("ExpireUploadsBy = %d, %v; want 1 (completed ones untouched)", n, err)
	}
	if _, err := s.CompleteUpload(ctx, pending.ID, uploaded(pending)); !errors.Is(err, dam.ErrUploadExpired) {
		t.Errorf("completing an expired session = %v, want ErrUploadExpired", err)
	}
	expired, _ := s.ListExpiredUploads(ctx, time.Now().Add(time.Second), 10)
	ids := make([]string, len(expired))
	for i, u := range expired {
		ids[i] = u.ID
	}
	if !slices.Equal(ids, []string{pending.ID}) {
		t.Errorf("expired sessions = %v, want just alice's pending one (not %s)", ids, bob.ID)
	}
}
