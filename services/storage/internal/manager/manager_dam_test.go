package manager_test

import (
	"encoding/base64"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"gyoza/services/storage/internal/dam"
	sm "gyoza/services/storage/internal/manager"
)

// damManager is a test Manager where "member" may write acme's files,
// "viewer" may read them, and "guest" may read project p1 (shared).
func damManager(t *testing.T) testManager {
	t.Helper()
	tm := newTestManager(t)
	tm.authz.AllowOwner("viewer", acme, sm.AccessRead)
	tm.authz.AllowProject("guest", "p1", sm.AccessRead)
	return tm
}

func TestManagerAssetOperations(t *testing.T) {
	tm := damManager(t)
	ctx := t.Context()
	a, _ := uploadAsset(t, tm, "member", newAssetRequest())
	folder, err := tm.CreateFolder(ctx, "member", dam.Folder{Owner: acme, Name: "Logos"})
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}

	// Readers can look, not change.
	if _, err := tm.GetAsset(ctx, "viewer", a.ID); err != nil {
		t.Errorf("viewer GetAsset: %v", err)
	}
	if _, err := tm.ListVersions(ctx, "viewer", a.ID); err != nil {
		t.Errorf("viewer ListVersions: %v", err)
	}
	writes := map[string]func(actor string) error{
		"update": func(actor string) error {
			_, err := tm.UpdateAsset(ctx, actor, a.ID, dam.AssetUpdate{Name: ptr("x")})
			return err
		},
		"delete":  func(actor string) error { return tm.DeleteAsset(ctx, actor, a.ID) },
		"restore": func(actor string) error { _, err := tm.RestoreAsset(ctx, actor, a.ID); return err },
		"revert":  func(actor string) error { _, err := tm.RevertAsset(ctx, actor, a.ID, 1); return err },
	}
	for name, write := range writes {
		if err := write("viewer"); !errors.Is(err, sm.ErrForbidden) {
			t.Errorf("viewer %s = %v, want ErrForbidden", name, err)
		}
	}
	if _, err := tm.GetAsset(ctx, "stranger", a.ID); !errors.Is(err, sm.ErrForbidden) {
		t.Errorf("stranger GetAsset = %v, want ErrForbidden", err)
	}

	// Members can do it all.
	updated, err := tm.UpdateAsset(ctx, "member", a.ID, dam.AssetUpdate{Name: ptr("Main logo"), Tags: ptr([]string{"Brand"}), FolderID: ptr(folder.ID)})
	if err != nil || updated.Name != "Main logo" || updated.FolderID != folder.ID || !slices.Equal(updated.Tags, []string{"brand"}) {
		t.Errorf("UpdateAsset = %+v, %v", updated, err)
	}
	ticket := beginAndPut(t, tm, "member", sm.UploadRequest{AssetID: a.ID, ContentType: "image/jpeg", SizeBytes: 100})
	if _, err := tm.CompleteUpload(ctx, "member", ticket.UploadID); err != nil {
		t.Fatalf("CompleteUpload: %v", err)
	}
	if reverted, err := tm.RevertAsset(ctx, "member", a.ID, 1); err != nil || reverted.CurrentVersion != 1 {
		t.Errorf("RevertAsset = %+v, %v", reverted, err)
	}
	if err := tm.DeleteAsset(ctx, "member", a.ID); err != nil {
		t.Fatalf("DeleteAsset: %v", err)
	}
	if restored, err := tm.RestoreAsset(ctx, "member", a.ID); err != nil || restored.DeletedAt != nil {
		t.Errorf("RestoreAsset = %+v, %v", restored, err)
	}
}

func TestManagerListAssets(t *testing.T) {
	tm := damManager(t)
	ctx := t.Context()
	tm.authz.AllowProject("member", "p1", sm.AccessWrite)
	inProject := newAssetRequest()
	inProject.ProjectID = "p1"
	p, _ := uploadAsset(t, tm, "member", inProject)
	lib, _ := uploadAsset(t, tm, "member", newAssetRequest())

	if page, err := tm.ListAssets(ctx, "guest", dam.AssetQuery{ProjectID: "p1"}); err != nil || !slices.Equal(assetIDs(page.Assets), []string{p.ID}) {
		t.Errorf("guest listing p1 = %v, %v; want [%s]", assetIDs(page.Assets), err, p.ID)
	}
	if page, err := tm.ListAssets(ctx, "viewer", dam.AssetQuery{Owner: acme}); err != nil || len(page.Assets) != 2 {
		t.Errorf("viewer listing acme = %v, %v; want both (incl. %s)", assetIDs(page.Assets), err, lib.ID)
	}
	if _, err := tm.ListAssets(ctx, "guest", dam.AssetQuery{Owner: acme}); !errors.Is(err, sm.ErrForbidden) {
		t.Errorf("guest listing all of acme = %v, want ErrForbidden", err)
	}
	if _, err := tm.ListAssets(ctx, "member", dam.AssetQuery{Owner: acme, ProjectID: "p1"}); !errors.Is(err, dam.ErrInvalidQuery) {
		t.Errorf("two scopes = %v, want ErrInvalidQuery", err)
	}
	// No scope is a bad request, not a permission problem.
	if _, err := tm.ListAssets(ctx, "member", dam.AssetQuery{}); !errors.Is(err, dam.ErrInvalidQuery) {
		t.Errorf("no scope = %v, want ErrInvalidQuery", err)
	}
}

// laterPageToken is a well-formed token for "a page after the first", for
// tests that need one without creating a page's worth of assets.
func laterPageToken() string {
	return base64.RawURLEncoding.EncodeToString([]byte(uuid.NewString()))
}

func TestManagerFolders(t *testing.T) {
	tm := damManager(t)
	ctx := t.Context()
	brand, err := tm.CreateFolder(ctx, "member", dam.Folder{Owner: acme, Name: "Brand", CreatedBy: "someone else"})
	if err != nil || brand.CreatedBy != "member" {
		t.Fatalf("CreateFolder = %+v, %v; want CreatedBy set to the actor", brand, err)
	}
	logos, _ := tm.CreateFolder(ctx, "member", dam.Folder{Owner: acme, Name: "Logos", ParentID: brand.ID})
	req := newAssetRequest()
	req.FolderID = logos.ID
	uploadAsset(t, tm, "member", req)

	if _, err := tm.CreateFolder(ctx, "viewer", dam.Folder{Owner: acme, Name: "Nope"}); !errors.Is(err, sm.ErrForbidden) {
		t.Errorf("viewer CreateFolder = %v, want ErrForbidden", err)
	}
	if path, err := tm.FolderPath(ctx, "viewer", logos.ID); err != nil || !slices.Equal(folderNames(path), []string{"Brand", "Logos"}) {
		t.Errorf("FolderPath = %q, %v", folderNames(path), err)
	}
	if f, err := tm.RenameFolder(ctx, "member", logos.ID, "Marks"); err != nil || f.Name != "Marks" {
		t.Errorf("RenameFolder = %+v, %v", f, err)
	}

	// Not empty: the error carries the counts for an "are you sure?" prompt.
	_, err = tm.DeleteFolder(ctx, "member", brand.ID)
	var notEmpty *dam.FolderNotEmptyError
	if !errors.As(err, &notEmpty) || notEmpty.Folders != 1 || notEmpty.Assets != 1 {
		t.Fatalf("DeleteFolder = %v, want FolderNotEmptyError{1 folder, 1 asset}", err)
	}
	del, err := tm.DeleteFolderTree(ctx, "member", brand.ID)
	if err != nil || del.Folders != 2 || del.Assets != 1 {
		t.Fatalf("DeleteFolderTree = %+v, %v", del, err)
	}
	if _, err := tm.RestoreFolder(ctx, "viewer", brand.ID); !errors.Is(err, sm.ErrForbidden) {
		t.Errorf("viewer RestoreFolder = %v, want ErrForbidden", err)
	}
	restored, err := tm.RestoreFolder(ctx, "member", brand.ID)
	if err != nil || restored.Folders != 2 || restored.Assets != 1 {
		t.Errorf("RestoreFolder = %+v, %v; want the whole tree back", restored, err)
	}
	if again, err := tm.RestoreFolder(ctx, "member", brand.ID); err != nil || again.Folders != 0 {
		t.Errorf("restoring a live folder = %+v, %v; want a no-op", again, err)
	}
	if moved, err := tm.MoveFolder(ctx, "member", logos.ID, ""); err != nil || moved.ParentID != "" {
		t.Errorf("MoveFolder to top = %+v, %v", moved, err)
	}
}

// A file browser screen: breadcrumbs, sub-folders (first page only), assets.
func TestManagerFolderContents(t *testing.T) {
	tm := damManager(t)
	ctx := t.Context()
	brand, _ := tm.CreateFolder(ctx, "member", dam.Folder{Owner: acme, Name: "Brand"})
	tm.CreateFolder(ctx, "member", dam.Folder{Owner: acme, Name: "Logos", ParentID: brand.ID})
	tm.CreateFolder(ctx, "member", dam.Folder{Owner: acme, Name: "Fonts", ParentID: brand.ID})
	var ids []string
	for range 3 {
		req := newAssetRequest()
		req.FolderID = brand.ID
		a, _ := uploadAsset(t, tm, "member", req)
		ids = append(ids, a.ID)
	}

	first, err := tm.Manager.ListFolderContents(ctx, "viewer", acme, brand.ID, "")
	if err != nil {
		t.Fatalf("ListFolderContents: %v", err)
	}
	if !slices.Equal(folderNames(first.Path), []string{"Brand"}) || !slices.Equal(folderNames(first.Folders), []string{"Fonts", "Logos"}) {
		t.Errorf("path %q, folders %q; want [Brand], [Fonts Logos]", folderNames(first.Path), folderNames(first.Folders))
	}
	if !slices.Equal(assetIDs(first.Assets.Assets), ids) {
		t.Errorf("assets = %v, want %v", assetIDs(first.Assets.Assets), ids)
	}

	top, err := tm.Manager.ListFolderContents(ctx, "viewer", acme, "", "")
	if err != nil || len(top.Path) != 0 || !slices.Equal(folderNames(top.Folders), []string{"Brand"}) || len(top.Assets.Assets) != 0 {
		t.Errorf("top level = %+v, %v", top, err)
	}
	// Later pages carry assets only: sub-folders came with the first page.
	if later, err := tm.Manager.ListFolderContents(ctx, "viewer", acme, brand.ID, laterPageToken()); err != nil || len(later.Folders) != 0 {
		t.Errorf("later page folders = %q, %v; want none", folderNames(later.Folders), err)
	}

	alicesFolder, _ := tm.CreateFolder(ctx, "alice", dam.Folder{Owner: alice, Name: "Mine"})
	for _, token := range []string{"", laterPageToken()} { // any page must not reveal her folder (e.g. via breadcrumbs)
		if _, err := tm.Manager.ListFolderContents(ctx, "member", acme, alicesFolder.ID, token); !errors.Is(err, dam.ErrFolderNotFound) {
			t.Errorf("another owner's folder (page token %q) = %v, want ErrFolderNotFound", token, err)
		}
	}
	if _, err := tm.Manager.ListFolderContents(ctx, "stranger", acme, "", ""); !errors.Is(err, sm.ErrForbidden) {
		t.Errorf("stranger = %v, want ErrForbidden", err)
	}
}

func TestManagerTrash(t *testing.T) {
	tm := damManager(t)
	ctx := t.Context()
	folder, _ := tm.CreateFolder(ctx, "member", dam.Folder{Owner: acme, Name: "Old"})
	a, _ := uploadAsset(t, tm, "member", newAssetRequest())
	if _, err := tm.DeleteFolder(ctx, "member", folder.ID); err != nil {
		t.Fatalf("DeleteFolder: %v", err)
	}
	if err := tm.DeleteAsset(ctx, "member", a.ID); err != nil {
		t.Fatalf("DeleteAsset: %v", err)
	}
	trash, err := tm.ListTrash(ctx, "viewer", acme, "")
	if err != nil || !slices.Equal(folderNames(trash.Folders), []string{"Old"}) || !slices.Equal(assetIDs(trash.Assets.Assets), []string{a.ID}) {
		t.Errorf("ListTrash = folders %q, assets %v, %v", folderNames(trash.Folders), assetIDs(trash.Assets.Assets), err)
	}
	if _, err := tm.ListTrash(ctx, "stranger", acme, ""); !errors.Is(err, sm.ErrForbidden) {
		t.Errorf("stranger ListTrash = %v, want ErrForbidden", err)
	}
}

// A project's own folders are reachable through the project's sharing.
func TestManagerProjectOwnedFolders(t *testing.T) {
	tm := damManager(t)
	ctx := t.Context()
	tm.authz.AllowProject("member", "p1", sm.AccessWrite)
	f, err := tm.CreateFolder(ctx, "member", dam.Folder{Owner: project, Name: "Scenes"})
	if err != nil {
		t.Fatalf("project member CreateFolder: %v", err)
	}
	if _, err := tm.FolderPath(ctx, "guest", f.ID); err != nil {
		t.Errorf("guest of p1 reading the project's folder: %v", err)
	}
	if _, err := tm.RenameFolder(ctx, "guest", f.ID, "x"); !errors.Is(err, sm.ErrForbidden) {
		t.Errorf("read-only guest renaming = %v, want ErrForbidden", err)
	}
}
