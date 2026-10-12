package manager_test

// Versions-by-number, breadcrumbs, trash listing and restore cases for the
// shared store suite (listed in runStoreTests).

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	sm "gyoza/services/storage/internal/manager"
)

func testGetVersion(t *testing.T, s sm.Store) {
	ctx := t.Context()
	a := mustCreateAsset(t, s, sm.Asset{Owner: alice}, "image/png")
	addVersions(t, s, a) // versions 1 (png), 2 (jpeg), 3 (webp)

	v, err := s.GetVersion(ctx, a.ID, 2)
	if err != nil || v.Number != 2 || v.ContentType != "image/jpeg" || v.AssetID != a.ID {
		t.Errorf("GetVersion(2) = %+v, %v", v, err)
	}
	for _, n := range []int{0, 4} {
		if _, err := s.GetVersion(ctx, a.ID, n); !errors.Is(err, sm.ErrVersionNotFound) {
			t.Errorf("GetVersion(%d) = %v, want ErrVersionNotFound", n, err)
		}
	}
	if _, err := s.GetVersion(ctx, uuid.NewString(), 1); !errors.Is(err, sm.ErrAssetNotFound) {
		t.Errorf("GetVersion of a missing asset = %v, want ErrAssetNotFound", err)
	}
}

func testListOnlyDeleted(t *testing.T, s sm.Store) {
	live := mustCreateAsset(t, s, sm.Asset{Owner: acme}, "image/png")
	gone := mustCreateAsset(t, s, sm.Asset{Owner: acme}, "image/png")
	if err := s.MarkAssetDeleted(t.Context(), gone.ID); err != nil {
		t.Fatalf("MarkAssetDeleted: %v", err)
	}
	page, err := s.ListAssets(t.Context(), sm.AssetQuery{Owner: acme, OnlyDeleted: true})
	if err != nil || !slices.Equal(assetIDs(page.Assets), []string{gone.ID}) {
		t.Errorf("OnlyDeleted = %v, %v; want only %s (not %s)", assetIDs(page.Assets), err, gone.ID, live.ID)
	}
}

func testRestoreAsset(t *testing.T, s sm.Store) {
	ctx := t.Context()
	folder := mustCreateFolder(t, s, sm.Folder{Owner: acme, Name: "Logos"})
	inFolder := mustCreateAsset(t, s, sm.Asset{Owner: acme, FolderID: folder.ID}, "image/png")
	if err := s.MarkAssetDeleted(ctx, inFolder.ID); err != nil {
		t.Fatalf("MarkAssetDeleted: %v", err)
	}
	restored, err := s.RestoreAsset(ctx, inFolder.ID)
	if err != nil || restored.DeletedAt != nil || restored.FolderID != folder.ID {
		t.Errorf("RestoreAsset = %+v, %v; want live again, still in Logos", restored, err)
	}
	if again, err := s.RestoreAsset(ctx, inFolder.ID); err != nil || again.DeletedAt != nil {
		t.Errorf("restoring a live asset = %+v, %v; want a no-op", again, err)
	}

	// Its folder went to the trash meanwhile: it comes back at the top level.
	orphan := mustCreateAsset(t, s, sm.Asset{Owner: acme, FolderID: folder.ID}, "image/png")
	if err := s.MarkAssetDeleted(ctx, orphan.ID); err != nil {
		t.Fatalf("MarkAssetDeleted: %v", err)
	}
	if err := s.MarkAssetDeleted(ctx, inFolder.ID); err != nil {
		t.Fatalf("MarkAssetDeleted: %v", err)
	}
	if _, err := s.DeleteFolder(ctx, folder.ID); err != nil {
		t.Fatalf("DeleteFolder: %v", err)
	}
	if got, err := s.RestoreAsset(ctx, orphan.ID); err != nil || got.FolderID != "" {
		t.Errorf("restore into a trashed folder = %+v, %v; want the top level", got, err)
	}
	if _, err := s.RestoreAsset(ctx, uuid.NewString()); !errors.Is(err, sm.ErrAssetNotFound) {
		t.Errorf("RestoreAsset of a missing asset = %v, want ErrAssetNotFound", err)
	}
}

func testFolderPath(t *testing.T, s sm.Store) {
	a := mustCreateFolder(t, s, sm.Folder{Owner: acme, Name: "Brand"})
	b := mustCreateFolder(t, s, sm.Folder{Owner: acme, Name: "Logos", ParentID: a.ID})
	c := mustCreateFolder(t, s, sm.Folder{Owner: acme, Name: "2026", ParentID: b.ID})

	path, err := s.FolderPath(t.Context(), c.ID)
	if err != nil || !slices.Equal(folderNames(path), []string{"Brand", "Logos", "2026"}) {
		t.Errorf("FolderPath = %q, %v; want [Brand Logos 2026]", folderNames(path), err)
	}
	if top, _ := s.FolderPath(t.Context(), a.ID); !slices.Equal(folderNames(top), []string{"Brand"}) {
		t.Errorf("FolderPath of a top-level folder = %q", folderNames(top))
	}
	if _, err := s.FolderPath(t.Context(), uuid.NewString()); !errors.Is(err, sm.ErrFolderNotFound) {
		t.Errorf("FolderPath of a missing folder = %v, want ErrFolderNotFound", err)
	}
}

func testListTrashedFolders(t *testing.T, s sm.Store) {
	ctx := t.Context()
	first := mustCreateFolder(t, s, sm.Folder{Owner: acme, Name: "First"})
	second := mustCreateFolder(t, s, sm.Folder{Owner: acme, Name: "Second"})
	mustCreateFolder(t, s, sm.Folder{Owner: acme, Name: "Live"})
	alices := mustCreateFolder(t, s, sm.Folder{Owner: alice, Name: "Alice's"})
	for _, f := range []sm.Folder{first, alices} {
		if _, err := s.DeleteFolder(ctx, f.ID); err != nil {
			t.Fatalf("DeleteFolder: %v", err)
		}
	}
	letTimePass()
	if _, err := s.DeleteFolder(ctx, second.ID); err != nil {
		t.Fatalf("DeleteFolder: %v", err)
	}
	got, err := s.ListTrashedFolders(ctx, acme)
	if err != nil || !slices.Equal(folderNames(got), []string{"Second", "First"}) {
		t.Errorf("ListTrashedFolders = %q, %v; want [Second First] (most recent first, acme's only)", folderNames(got), err)
	}
}

// Undoing an rm -rf puts everything back where it was.
func testRestoreDeletion(t *testing.T, s sm.Store) {
	ctx := t.Context()
	brand := mustCreateFolder(t, s, sm.Folder{Owner: acme, Name: "Brand"})
	logos := mustCreateFolder(t, s, sm.Folder{Owner: acme, Name: "Logos", ParentID: brand.ID})
	inBrand := mustCreateAsset(t, s, sm.Asset{Owner: acme, FolderID: brand.ID}, "image/png")
	inLogos := mustCreateAsset(t, s, sm.Asset{Owner: acme, FolderID: logos.ID}, "image/png")
	separately := mustCreateAsset(t, s, sm.Asset{Owner: acme, FolderID: logos.ID}, "image/png")
	if err := s.MarkAssetDeleted(ctx, separately.ID); err != nil { // trashed on its own, earlier
		t.Fatalf("MarkAssetDeleted: %v", err)
	}
	del, err := s.DeleteFolderTree(ctx, brand.ID)
	if err != nil {
		t.Fatalf("DeleteFolderTree: %v", err)
	}

	restored, err := s.RestoreDeletion(ctx, del.DeletionID)
	if err != nil || restored.Folders != 2 || restored.Assets != 2 {
		t.Fatalf("RestoreDeletion = %+v, %v; want 2 folders and 2 assets", restored, err)
	}
	if got := mustGetFolder(t, s, logos.ID); got.DeletedAt != nil || got.ParentID != brand.ID || got.DeletionID != "" {
		t.Errorf("Logos = %+v; want live again inside Brand", got)
	}
	for _, a := range []sm.Asset{inBrand, inLogos} {
		if got := mustGetAsset(t, s, a.ID); got.DeletedAt != nil || got.FolderID != a.FolderID {
			t.Errorf("asset %s = %+v; want live again in its folder", a.ID, got)
		}
	}
	if got := mustGetAsset(t, s, separately.ID); got.DeletedAt == nil {
		t.Error("an asset trashed separately came back with the rm -rf undo")
	}
	if again, err := s.RestoreDeletion(ctx, del.DeletionID); err != nil || again.Folders != 0 || again.Assets != 0 {
		t.Errorf("restoring twice = %+v, %v; want nothing left to restore", again, err)
	}
	if _, err := s.RestoreDeletion(ctx, "garbage"); !errors.Is(err, sm.ErrInvalidFolder) {
		t.Errorf("RestoreDeletion(garbage) = %v, want ErrInvalidFolder", err)
	}
}

// A restored folder whose name is now taken is numbered; one whose parent
// was deleted separately lands at the top level.
func testRestoreDeletionResolvesConflicts(t *testing.T, s sm.Store) {
	ctx := t.Context()
	brand := mustCreateFolder(t, s, sm.Folder{Owner: acme, Name: "Brand"})
	del, err := s.DeleteFolderTree(ctx, brand.ID)
	if err != nil {
		t.Fatalf("DeleteFolderTree: %v", err)
	}
	mustCreateFolder(t, s, sm.Folder{Owner: acme, Name: "brand"}) // a new live "brand" in its place
	if _, err := s.RestoreDeletion(ctx, del.DeletionID); err != nil {
		t.Fatalf("RestoreDeletion: %v", err)
	}
	if got := mustGetFolder(t, s, brand.ID); got.Name != "Brand (2)" {
		t.Errorf("restored folder named %q, want %q", got.Name, "Brand (2)")
	}

	parent := mustCreateFolder(t, s, sm.Folder{Owner: acme, Name: "Parent"})
	child := mustCreateFolder(t, s, sm.Folder{Owner: acme, Name: "Child", ParentID: parent.ID})
	childDel, err := s.DeleteFolderTree(ctx, child.ID)
	if err != nil {
		t.Fatalf("DeleteFolderTree: %v", err)
	}
	if _, err := s.DeleteFolder(ctx, parent.ID); err != nil { // now empty, deleted on its own
		t.Fatalf("DeleteFolder: %v", err)
	}
	if _, err := s.RestoreDeletion(ctx, childDel.DeletionID); err != nil {
		t.Fatalf("RestoreDeletion: %v", err)
	}
	if got := mustGetFolder(t, s, child.ID); got.DeletedAt != nil || got.ParentID != "" {
		t.Errorf("child = %+v; want restored to the top level, its parent being trashed", got)
	}
}
