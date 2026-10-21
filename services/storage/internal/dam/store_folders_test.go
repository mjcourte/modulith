package dam_test

// Folder cases for the shared store suite (listed in runStoreTests).

import (
	"errors"
	"github.com/mjcourte/modulith/services/storage/internal/dam"
	"slices"
	"testing"

	"github.com/google/uuid"
)

func mustCreateFolder(t *testing.T, s dam.Store, f dam.Folder) dam.Folder {
	t.Helper()
	created, err := s.CreateFolder(t.Context(), f)
	if err != nil {
		t.Fatalf("CreateFolder(%q): %v", f.Name, err)
	}
	return created
}

func mustGetFolder(t *testing.T, s dam.Store, id string) dam.Folder {
	t.Helper()
	f, err := s.GetFolder(t.Context(), id)
	if err != nil {
		t.Fatalf("GetFolder: %v", err)
	}
	return f
}

// folderNames lists the names of folders, for readable comparisons.
func folderNames(folders []dam.Folder) []string {
	out := make([]string, len(folders))
	for i, f := range folders {
		out[i] = f.Name
	}
	return out
}

// mustListFolders returns the names of parent's live children in owner's tree.
func mustListFolders(t *testing.T, s dam.Store, parentID string) []string {
	t.Helper()
	folders, err := s.ListFolders(t.Context(), acme, parentID)
	if err != nil {
		t.Fatalf("ListFolders: %v", err)
	}
	return folderNames(folders)
}

// folderChain creates n nested folders for acme; chain[i] sits at depth i+1.
func folderChain(t *testing.T, s dam.Store, n int) []dam.Folder {
	t.Helper()
	chain := make([]dam.Folder, n)
	parent := ""
	for i := range chain {
		chain[i] = mustCreateFolder(t, s, dam.Folder{Owner: acme, Name: "level", ParentID: parent})
		parent = chain[i].ID
	}
	return chain
}

func testFolderCreateThenGet(t *testing.T, s dam.Store) {
	created := mustCreateFolder(t, s, dam.Folder{Owner: acme, Name: "  Brand  ", CreatedBy: "alice"})
	if u, err := uuid.Parse(created.ID); err != nil || u.Version() != 7 {
		t.Errorf("ID %q is not a UUIDv7", created.ID)
	}
	if created.Name != "Brand" || created.ParentID != "" || created.CreatedAt.IsZero() ||
		!created.UpdatedAt.Equal(created.CreatedAt) || created.DeletedAt != nil {
		t.Errorf("created = %+v", created)
	}
	child := mustCreateFolder(t, s, dam.Folder{Owner: acme, Name: "Logos", ParentID: created.ID})
	if got := mustGetFolder(t, s, child.ID); got.ParentID != created.ID || got.Name != "Logos" || got.Owner != acme {
		t.Errorf("GetFolder = %+v", got)
	}
	if _, err := s.GetFolder(t.Context(), uuid.NewString()); !errors.Is(err, dam.ErrFolderNotFound) {
		t.Errorf("GetFolder of missing folder = %v, want ErrFolderNotFound", err)
	}
}

func testFolderCreateRejects(t *testing.T, s dam.Store) {
	ctx := t.Context()
	alicesFolder := mustCreateFolder(t, s, dam.Folder{Owner: alice, Name: "Mine"})
	tests := []struct {
		name   string
		folder dam.Folder
		want   error
	}{
		{"no owner", dam.Folder{Name: "x"}, dam.ErrInvalidFolder},
		{"non-portable name", dam.Folder{Owner: acme, Name: "a/b"}, dam.ErrInvalidFolder},
		{"reserved name", dam.Folder{Owner: acme, Name: "CON"}, dam.ErrInvalidFolder},
		{"missing parent", dam.Folder{Owner: acme, Name: "x", ParentID: uuid.NewString()}, dam.ErrFolderNotFound},
		{"other owner's parent", dam.Folder{Owner: acme, Name: "x", ParentID: alicesFolder.ID}, dam.ErrFolderNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := s.CreateFolder(ctx, tt.folder); !errors.Is(err, tt.want) {
				t.Errorf("CreateFolder = %v, want %v", err, tt.want)
			}
		})
	}
}

func testFolderNamesUniqueIgnoringCase(t *testing.T, s dam.Store) {
	ctx := t.Context()
	brand := mustCreateFolder(t, s, dam.Folder{Owner: acme, Name: "Logos"})
	if _, err := s.CreateFolder(ctx, dam.Folder{Owner: acme, Name: "LOGOS"}); !errors.Is(err, dam.ErrNameTaken) {
		t.Errorf("sibling differing only in case = %v, want ErrNameTaken", err)
	}
	// The same name is fine elsewhere: in another folder, or another owner's tree.
	mustCreateFolder(t, s, dam.Folder{Owner: acme, Name: "logos", ParentID: brand.ID})
	mustCreateFolder(t, s, dam.Folder{Owner: alice, Name: "logos"})
}

func testFolderDepthLimit(t *testing.T, s dam.Store) {
	chain := folderChain(t, s, dam.MaxFolderDepth)
	deepest := chain[len(chain)-1]
	_, err := s.CreateFolder(t.Context(), dam.Folder{Owner: acme, Name: "too deep", ParentID: deepest.ID})
	if !errors.Is(err, dam.ErrInvalidFolder) {
		t.Errorf("folder at depth %d = %v, want ErrInvalidFolder", dam.MaxFolderDepth+1, err)
	}
}

func testListFolders(t *testing.T, s dam.Store) {
	ctx := t.Context()
	for _, name := range []string{"b", "A", "c"} {
		mustCreateFolder(t, s, dam.Folder{Owner: acme, Name: name})
	}
	a := mustListFolders(t, s, "")
	if want := []string{"A", "b", "c"}; !slices.Equal(a, want) {
		t.Errorf("top level = %q, want %q (by name, ignoring case)", a, want)
	}
	top, _ := s.ListFolders(ctx, acme, "")
	parent := top[0]
	mustCreateFolder(t, s, dam.Folder{Owner: acme, Name: "child", ParentID: parent.ID})
	mustCreateFolder(t, s, dam.Folder{Owner: alice, Name: "not acme's"})

	if got := mustListFolders(t, s, ""); len(got) != 3 {
		t.Errorf("top level now %q: sub-folders and other owners' folders must not be listed", got)
	}
	if got := mustListFolders(t, s, parent.ID); !slices.Equal(got, []string{"child"}) {
		t.Errorf("children of %s = %q, want [child]", parent.Name, got)
	}
	if _, err := s.ListFolders(ctx, acme, uuid.NewString()); !errors.Is(err, dam.ErrFolderNotFound) {
		t.Errorf("ListFolders of missing parent = %v, want ErrFolderNotFound", err)
	}
	if _, err := s.ListFolders(ctx, alice, parent.ID); !errors.Is(err, dam.ErrFolderNotFound) {
		t.Errorf("ListFolders of another owner's folder = %v, want ErrFolderNotFound", err)
	}
}

func testRenameFolder(t *testing.T, s dam.Store) {
	ctx := t.Context()
	f := mustCreateFolder(t, s, dam.Folder{Owner: acme, Name: "logos"})
	mustCreateFolder(t, s, dam.Folder{Owner: acme, Name: "Icons"})

	letTimePass()
	renamed, err := s.RenameFolder(ctx, f.ID, " Logos ") // changing only the case is fine
	if err != nil {
		t.Fatalf("RenameFolder: %v", err)
	}
	if renamed.Name != "Logos" || !renamed.UpdatedAt.After(f.UpdatedAt) {
		t.Errorf("renamed = %+v", renamed)
	}
	if _, err := s.RenameFolder(ctx, f.ID, "icons"); !errors.Is(err, dam.ErrNameTaken) {
		t.Errorf("rename onto a sibling's name = %v, want ErrNameTaken", err)
	}
	if _, err := s.RenameFolder(ctx, f.ID, "a:b"); !errors.Is(err, dam.ErrInvalidFolder) {
		t.Errorf("rename to a non-portable name = %v, want ErrInvalidFolder", err)
	}
	if _, err := s.RenameFolder(ctx, uuid.NewString(), "x"); !errors.Is(err, dam.ErrFolderNotFound) {
		t.Errorf("rename of missing folder = %v, want ErrFolderNotFound", err)
	}
	if got := mustGetFolder(t, s, f.ID); got.Name != "Logos" {
		t.Errorf("rejected renames changed the name to %q", got.Name)
	}
}

func testMoveFolder(t *testing.T, s dam.Store) {
	ctx := t.Context()
	brand := mustCreateFolder(t, s, dam.Folder{Owner: acme, Name: "Brand"})
	logos := mustCreateFolder(t, s, dam.Folder{Owner: acme, Name: "Logos"})
	old := mustCreateFolder(t, s, dam.Folder{Owner: acme, Name: "Old", ParentID: logos.ID})

	moved, err := s.MoveFolder(ctx, logos.ID, brand.ID)
	if err != nil || moved.ParentID != brand.ID {
		t.Fatalf("MoveFolder into Brand = %+v, %v", moved, err)
	}
	if got := mustListFolders(t, s, brand.ID); !slices.Equal(got, []string{"Logos"}) {
		t.Errorf("Brand now holds %q, want [Logos]", got)
	}
	if got := mustGetFolder(t, s, old.ID); got.ParentID != logos.ID {
		t.Errorf("sub-folder's parent changed to %q; it should move along unchanged", got.ParentID)
	}
	if same, err := s.MoveFolder(ctx, logos.ID, brand.ID); err != nil || same.ParentID != brand.ID {
		t.Errorf("move to the current parent = %+v, %v; want a no-op", same, err)
	}
	if top, err := s.MoveFolder(ctx, old.ID, ""); err != nil || top.ParentID != "" {
		t.Errorf("move to top level = %+v, %v", top, err)
	}

	// Brand > Logos: moving Brand into itself or into Logos would make a loop.
	// Fatalf, not Errorf: if a cycle was created, later calls that walk up the
	// tree would never finish, hiding this failure behind a test timeout.
	for _, target := range []dam.Folder{brand, logos} {
		if _, err := s.MoveFolder(ctx, brand.ID, target.ID); !errors.Is(err, dam.ErrInvalidFolder) {
			t.Fatalf("move Brand into %s = %v, want ErrInvalidFolder", target.Name, err)
		}
	}
	if got := mustGetFolder(t, s, brand.ID); got.ParentID != "" {
		t.Errorf("rejected cycle moved Brand under %q", got.ParentID)
	}

	alices := mustCreateFolder(t, s, dam.Folder{Owner: alice, Name: "Alice's"})
	if _, err := s.MoveFolder(ctx, brand.ID, alices.ID); !errors.Is(err, dam.ErrFolderNotFound) {
		t.Errorf("move into another owner's folder = %v, want ErrFolderNotFound", err)
	}
	if _, err := s.MoveFolder(ctx, brand.ID, uuid.NewString()); !errors.Is(err, dam.ErrFolderNotFound) {
		t.Errorf("move into a missing folder = %v, want ErrFolderNotFound", err)
	}

	mustCreateFolder(t, s, dam.Folder{Owner: acme, Name: "old", ParentID: brand.ID})
	if _, err := s.MoveFolder(ctx, old.ID, brand.ID); !errors.Is(err, dam.ErrNameTaken) {
		t.Errorf("move next to a same-named sibling = %v, want ErrNameTaken", err)
	}
}

// A move takes the folder's whole subtree along, so the deepest sub-folder
// must still fit within MaxFolderDepth.
func testMoveFolderDepthCountsSubtree(t *testing.T, s dam.Store) {
	ctx := t.Context()
	chain := folderChain(t, s, dam.MaxFolderDepth-1)
	parentAtMaxMinus1 := chain[dam.MaxFolderDepth-2] // depth Max-1
	parentAtMaxMinus2 := chain[dam.MaxFolderDepth-3] // depth Max-2

	tall := mustCreateFolder(t, s, dam.Folder{Owner: acme, Name: "tall"})
	mustCreateFolder(t, s, dam.Folder{Owner: acme, Name: "child", ParentID: tall.ID}) // tall spans 2 levels

	if _, err := s.MoveFolder(ctx, tall.ID, parentAtMaxMinus1.ID); !errors.Is(err, dam.ErrInvalidFolder) {
		t.Errorf("move putting the child at depth %d = %v, want ErrInvalidFolder", dam.MaxFolderDepth+1, err)
	}
	if _, err := s.MoveFolder(ctx, tall.ID, parentAtMaxMinus2.ID); err != nil {
		t.Errorf("move putting the child at depth %d = %v, want success", dam.MaxFolderDepth, err)
	}
}

func testAssetsInFolders(t *testing.T, s dam.Store) {
	ctx := t.Context()
	logos := mustCreateFolder(t, s, dam.Folder{Owner: acme, Name: "Logos"})
	alices := mustCreateFolder(t, s, dam.Folder{Owner: alice, Name: "Alice's"})

	inLogos := mustCreateAsset(t, s, dam.Asset{Owner: acme, FolderID: logos.ID}, "image/png")
	atTop := mustCreateAsset(t, s, dam.Asset{Owner: acme}, "image/png")
	if _, err := s.CreateAsset(ctx, dam.Asset{Owner: acme, Name: "x", FolderID: alices.ID}, version("image/png")); !errors.Is(err, dam.ErrFolderNotFound) {
		t.Errorf("asset in another owner's folder = %v, want ErrFolderNotFound", err)
	}

	list := func(folder *string) []string {
		t.Helper()
		page, err := s.ListAssets(ctx, dam.AssetQuery{Owner: acme, Folder: folder})
		if err != nil {
			t.Fatalf("ListAssets: %v", err)
		}
		return assetIDs(page.Assets)
	}
	if got := list(ptr(logos.ID)); !slices.Equal(got, []string{inLogos.ID}) {
		t.Errorf("assets in Logos = %v, want [%s]", got, inLogos.ID)
	}
	if got := list(ptr("")); !slices.Equal(got, []string{atTop.ID}) {
		t.Errorf("assets at top level = %v, want [%s]", got, atTop.ID)
	}
	if got := list(nil); len(got) != 2 {
		t.Errorf("assets in any folder = %v, want both", got)
	}

	moved, err := s.UpdateAsset(ctx, atTop.ID, dam.AssetUpdate{FolderID: ptr(logos.ID)})
	if err != nil || moved.FolderID != logos.ID {
		t.Fatalf("move asset into Logos = %+v, %v", moved, err)
	}
	if _, err := s.UpdateAsset(ctx, atTop.ID, dam.AssetUpdate{Name: ptr("renamed"), FolderID: ptr(alices.ID)}); !errors.Is(err, dam.ErrFolderNotFound) {
		t.Errorf("move into another owner's folder = %v, want ErrFolderNotFound", err)
	}
	if got := mustGetAsset(t, s, atTop.ID); got.FolderID != logos.ID || got.Name == "renamed" {
		t.Errorf("rejected move changed the asset: %+v", got)
	}
	if back, err := s.UpdateAsset(ctx, atTop.ID, dam.AssetUpdate{FolderID: ptr("")}); err != nil || back.FolderID != "" {
		t.Errorf("move back to top level = %+v, %v", back, err)
	}
}

func testDeleteEmptyFolder(t *testing.T, s dam.Store) {
	ctx := t.Context()
	f := mustCreateFolder(t, s, dam.Folder{Owner: acme, Name: "Empty"})
	del, err := s.DeleteFolder(ctx, f.ID)
	if err != nil || del.Folders != 1 || del.Assets != 0 || del.DeletionID == "" {
		t.Fatalf("DeleteFolder = %+v, %v", del, err)
	}
	trashed := mustGetFolder(t, s, f.ID) // still readable from the trash
	if trashed.DeletedAt == nil || trashed.DeletionID != del.DeletionID {
		t.Errorf("trashed folder = %+v", trashed)
	}
	if got := mustListFolders(t, s, ""); len(got) != 0 {
		t.Errorf("trashed folder still listed: %q", got)
	}
	mustCreateFolder(t, s, dam.Folder{Owner: acme, Name: "empty"}) // the name is free again

	letTimePass()
	if again, err := s.DeleteFolder(ctx, f.ID); err != nil || again.Folders != 0 {
		t.Errorf("second DeleteFolder = %+v, %v; want a no-op", again, err)
	}
	if got := mustGetFolder(t, s, f.ID); !got.DeletedAt.Equal(*trashed.DeletedAt) {
		t.Errorf("second delete changed DeletedAt from %v to %v", *trashed.DeletedAt, *got.DeletedAt)
	}
	if _, err := s.DeleteFolder(ctx, uuid.NewString()); !errors.Is(err, dam.ErrFolderNotFound) {
		t.Errorf("DeleteFolder of missing folder = %v, want ErrFolderNotFound", err)
	}
}

func testDeleteFolderNotEmpty(t *testing.T, s dam.Store) {
	ctx := t.Context()
	brand := mustCreateFolder(t, s, dam.Folder{Owner: acme, Name: "Brand"})
	logos := mustCreateFolder(t, s, dam.Folder{Owner: acme, Name: "Logos", ParentID: brand.ID})
	old := mustCreateFolder(t, s, dam.Folder{Owner: acme, Name: "Old", ParentID: logos.ID})
	mustCreateAsset(t, s, dam.Asset{Owner: acme, FolderID: brand.ID}, "image/png")
	mustCreateAsset(t, s, dam.Asset{Owner: acme, FolderID: old.ID}, "image/png") // two levels down
	gone := mustCreateAsset(t, s, dam.Asset{Owner: acme, FolderID: logos.ID}, "image/png")
	if err := s.MarkAssetDeleted(ctx, gone.ID); err != nil { // already trashed: doesn't count
		t.Fatalf("MarkAssetDeleted: %v", err)
	}

	_, err := s.DeleteFolder(ctx, brand.ID)
	var notEmpty *dam.FolderNotEmptyError
	if !errors.As(err, &notEmpty) || !errors.Is(err, dam.ErrFolderNotEmpty) {
		t.Fatalf("DeleteFolder = %v, want a *FolderNotEmptyError matching ErrFolderNotEmpty", err)
	}
	if notEmpty.Folders != 2 || notEmpty.Assets != 2 {
		t.Errorf("FolderNotEmptyError = %+v, want 2 folders and 2 assets (at any depth, live only)", notEmpty)
	}
	if got := mustGetFolder(t, s, brand.ID); got.DeletedAt != nil {
		t.Error("refused delete trashed the folder anyway")
	}

	// A folder whose only content is already in the trash counts as empty.
	solo := mustCreateFolder(t, s, dam.Folder{Owner: acme, Name: "Solo"})
	a := mustCreateAsset(t, s, dam.Asset{Owner: acme, FolderID: solo.ID}, "image/png")
	if err := s.MarkAssetDeleted(ctx, a.ID); err != nil {
		t.Fatalf("MarkAssetDeleted: %v", err)
	}
	if _, err := s.DeleteFolder(ctx, solo.ID); err != nil {
		t.Errorf("folder holding only trashed assets: %v, want deleted", err)
	}
}

// rm -rf: the folder, its sub-folders at any depth and their live assets go to
// the trash together under one DeletionID, keeping their places in the tree.
func testDeleteFolderTree(t *testing.T, s dam.Store) {
	ctx := t.Context()
	brand := mustCreateFolder(t, s, dam.Folder{Owner: acme, Name: "Brand"})
	logos := mustCreateFolder(t, s, dam.Folder{Owner: acme, Name: "Logos", ParentID: brand.ID})
	old := mustCreateFolder(t, s, dam.Folder{Owner: acme, Name: "Old", ParentID: logos.ID})
	sibling := mustCreateFolder(t, s, dam.Folder{Owner: acme, Name: "Sibling"})
	inBrand := mustCreateAsset(t, s, dam.Asset{Owner: acme, FolderID: brand.ID}, "image/png")
	inOld := mustCreateAsset(t, s, dam.Asset{Owner: acme, FolderID: old.ID}, "image/png")
	outside := mustCreateAsset(t, s, dam.Asset{Owner: acme, FolderID: sibling.ID}, "image/png")
	trashedBefore := mustCreateAsset(t, s, dam.Asset{Owner: acme, FolderID: logos.ID}, "image/png")
	if err := s.MarkAssetDeleted(ctx, trashedBefore.ID); err != nil {
		t.Fatalf("MarkAssetDeleted: %v", err)
	}
	earlierDeletion := mustGetAsset(t, s, trashedBefore.ID).DeletedAt

	letTimePass()
	del, err := s.DeleteFolderTree(ctx, brand.ID)
	if err != nil {
		t.Fatalf("DeleteFolderTree: %v", err)
	}
	if del.Folders != 3 || del.Assets != 2 || del.DeletionID == "" {
		t.Errorf("DeleteFolderTree = %+v, want 3 folders and 2 assets", del)
	}

	for _, f := range []dam.Folder{brand, logos, old} {
		got := mustGetFolder(t, s, f.ID)
		if got.DeletedAt == nil || got.DeletionID != del.DeletionID || got.ParentID != f.ParentID {
			t.Errorf("folder %s = %+v; want trashed under %s with its parent kept", f.Name, got, del.DeletionID)
		}
	}
	for _, a := range []dam.Asset{inBrand, inOld} {
		got := mustGetAsset(t, s, a.ID)
		if got.DeletedAt == nil || got.DeletionID != del.DeletionID || got.FolderID != a.FolderID {
			t.Errorf("asset %s = %+v; want trashed under %s with its folder kept", a.ID, got, del.DeletionID)
		}
	}
	// Things deleted separately keep their own deletion, so undoing the
	// rm -rf won't bring them back.
	if got := mustGetAsset(t, s, trashedBefore.ID); got.DeletionID != "" || !got.DeletedAt.Equal(*earlierDeletion) {
		t.Errorf("previously trashed asset was re-stamped: %+v", got)
	}
	if got := mustGetAsset(t, s, outside.ID); got.DeletedAt != nil {
		t.Error("asset outside the tree was trashed")
	}
	if got := mustGetFolder(t, s, sibling.ID); got.DeletedAt != nil {
		t.Error("folder outside the tree was trashed")
	}
	if got := mustListFolders(t, s, ""); !slices.Equal(got, []string{"Sibling"}) {
		t.Errorf("top level after rm -rf = %q, want [Sibling]", got)
	}

	if again, err := s.DeleteFolderTree(ctx, brand.ID); err != nil || again.Folders != 0 || again.Assets != 0 {
		t.Errorf("second DeleteFolderTree = %+v, %v; want a no-op", again, err)
	}
	if _, err := s.DeleteFolderTree(ctx, uuid.NewString()); !errors.Is(err, dam.ErrFolderNotFound) {
		t.Errorf("DeleteFolderTree of missing folder = %v, want ErrFolderNotFound", err)
	}
}

// A trashed folder can be read but not used: nothing can be put in it, and
// it can't be renamed or moved.
func testTrashedFoldersAreReadOnly(t *testing.T, s dam.Store) {
	ctx := t.Context()
	trashed := mustCreateFolder(t, s, dam.Folder{Owner: acme, Name: "Trashed"})
	live := mustCreateFolder(t, s, dam.Folder{Owner: acme, Name: "Live"})
	asset := mustCreateAsset(t, s, dam.Asset{Owner: acme}, "image/png")
	if _, err := s.DeleteFolder(ctx, trashed.ID); err != nil {
		t.Fatalf("DeleteFolder: %v", err)
	}

	checks := map[string]error{}
	_, checks["rename"] = s.RenameFolder(ctx, trashed.ID, "x")
	_, checks["move it"] = s.MoveFolder(ctx, trashed.ID, live.ID)
	_, checks["move a folder into it"] = s.MoveFolder(ctx, live.ID, trashed.ID)
	_, checks["create a folder in it"] = s.CreateFolder(ctx, dam.Folder{Owner: acme, Name: "x", ParentID: trashed.ID})
	_, checks["create an asset in it"] = s.CreateAsset(ctx, dam.Asset{Owner: acme, Name: "x", FolderID: trashed.ID}, version("image/png"))
	_, checks["move an asset into it"] = s.UpdateAsset(ctx, asset.ID, dam.AssetUpdate{FolderID: ptr(trashed.ID)})
	_, checks["list its children"] = s.ListFolders(ctx, acme, trashed.ID)
	for what, err := range checks {
		if !errors.Is(err, dam.ErrFolderNotFound) {
			t.Errorf("%s: got %v, want ErrFolderNotFound", what, err)
		}
	}
}
