package manager_test

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	sm "gyoza/services/storage/internal/manager"
)

func mustTrashAsset(t *testing.T, s sm.Store, id string) {
	t.Helper()
	if err := s.MarkAssetDeleted(t.Context(), id); err != nil {
		t.Fatalf("MarkAssetDeleted: %v", err)
	}
}

func testListAssetsTrashedBefore(t *testing.T, s sm.Store) {
	ctx := t.Context()
	first := mustCreateAsset(t, s, sm.Asset{Owner: alice, Name: "first"}, "image/png")
	mustCreateAsset(t, s, sm.Asset{Owner: alice, Name: "live"}, "image/png")
	// Trashed in the reverse of creation order, so ID order can't pass for
	// deletion order; several of them, so map order can't pass by luck.
	var rest []sm.Asset
	for i := range 5 {
		rest = append(rest, mustCreateAsset(t, s, sm.Asset{Owner: acme, Name: fmt.Sprint("later ", i)}, "image/png"))
	}

	mustTrashAsset(t, s, first.ID)
	letTimePass()
	between := time.Now()
	letTimePass()
	wantAll := []string{first.ID}
	for _, a := range slices.Backward(rest) {
		mustTrashAsset(t, s, a.ID)
		wantAll = append(wantAll, a.ID)
		letTimePass()
	}
	later := time.Now().Add(time.Minute)

	tests := []struct {
		before time.Time
		limit  int
		want   []string
	}{
		{between, 10, []string{first.ID}},
		{later, 10, wantAll}, // longest-trashed first, any owner
		{later, 1, []string{first.ID}},
	}
	for _, tt := range tests {
		got, err := s.ListAssetsTrashedBefore(ctx, tt.before, tt.limit)
		if err != nil {
			t.Fatalf("ListAssetsTrashedBefore: %v", err)
		}
		if !slices.Equal(assetIDs(got), tt.want) {
			t.Errorf("ListAssetsTrashedBefore(%v, %d) = %v, want %v", tt.before, tt.limit, assetIDs(got), tt.want)
		}
	}
	for _, limit := range []int{0, sm.MaxPageSize + 1} {
		if _, err := s.ListAssetsTrashedBefore(ctx, later, limit); !errors.Is(err, sm.ErrInvalidQuery) {
			t.Errorf("limit %d: got %v, want ErrInvalidQuery", limit, err)
		}
	}
}

func testPurgeAsset(t *testing.T, s sm.Store) {
	ctx := t.Context()
	a := mustCreateAsset(t, s, sm.Asset{Owner: alice, Name: "photo"}, "image/png")
	if _, err := s.AddVersion(ctx, a.ID, version("image/jpeg")); err != nil {
		t.Fatalf("AddVersion: %v", err)
	}

	if err := s.PurgeAsset(ctx, a.ID); !errors.Is(err, sm.ErrInvalidAsset) {
		t.Fatalf("PurgeAsset(live) = %v, want ErrInvalidAsset", err)
	}
	if _, err := s.GetAsset(ctx, a.ID); err != nil {
		t.Fatalf("live asset gone after a refused purge: %v", err)
	}

	mustTrashAsset(t, s, a.ID)
	if err := s.PurgeAsset(ctx, a.ID); err != nil {
		t.Fatalf("PurgeAsset: %v", err)
	}
	if _, err := s.GetAsset(ctx, a.ID); !errors.Is(err, sm.ErrAssetNotFound) {
		t.Errorf("GetAsset after purge = %v, want ErrAssetNotFound", err)
	}
	if _, err := s.GetVersion(ctx, a.ID, 1); err == nil {
		t.Error("version 1 still readable after purge")
	}
	page, err := s.ListAssets(ctx, sm.AssetQuery{Owner: alice, IncludeDeleted: true})
	if err != nil || len(page.Assets) != 0 {
		t.Errorf("ListAssets after purge = %v, %v; want nothing", assetIDs(page.Assets), err)
	}
	for _, id := range []string{a.ID, uuid.NewString()} {
		if err := s.PurgeAsset(ctx, id); err != nil {
			t.Errorf("PurgeAsset(%s) again = %v, want nil", id, err)
		}
	}
}

func testPurgeFolders(t *testing.T, s sm.Store) {
	ctx := t.Context()
	parent := mustCreateFolder(t, s, sm.Folder{Owner: alice, Name: "Parent"})
	child := mustCreateFolder(t, s, sm.Folder{Owner: alice, Name: "Child", ParentID: parent.ID})
	inChild := mustCreateAsset(t, s, sm.Asset{Owner: alice, Name: "inside", FolderID: child.ID}, "image/png")
	live := mustCreateFolder(t, s, sm.Folder{Owner: alice, Name: "Live"})
	if _, err := s.DeleteFolderTree(ctx, parent.ID); err != nil {
		t.Fatalf("DeleteFolderTree: %v", err)
	}
	later := time.Now().Add(time.Minute)

	// Parent and child were trashed together; the parent is older by ID, so
	// a batch of one purges it alone and its child moves to the top level.
	if n, err := s.PurgeFolders(ctx, later, 1); err != nil || n != 1 {
		t.Fatalf("PurgeFolders(limit 1) = %d, %v; want 1", n, err)
	}
	if _, err := s.GetFolder(ctx, parent.ID); !errors.Is(err, sm.ErrFolderNotFound) {
		t.Errorf("GetFolder(parent) after purge = %v, want ErrFolderNotFound", err)
	}
	if c, err := s.GetFolder(ctx, child.ID); err != nil || c.ParentID != "" || c.DeletedAt == nil {
		t.Errorf("child after its parent's purge = %+v, %v; want trashed at the top level", c, err)
	}

	if n, err := s.PurgeFolders(ctx, later, 10); err != nil || n != 1 {
		t.Fatalf("PurgeFolders = %d, %v; want the child", n, err)
	}
	if a, err := s.GetAsset(ctx, inChild.ID); err != nil || a.FolderID != "" || a.DeletedAt == nil {
		t.Errorf("asset after its folder's purge = %+v, %v; want trashed at the top level", a, err)
	}
	if _, err := s.GetFolder(ctx, live.ID); err != nil {
		t.Errorf("live folder purged: %v", err)
	}
	if n, err := s.PurgeFolders(ctx, later, 10); err != nil || n != 0 {
		t.Errorf("PurgeFolders with nothing due = %d, %v; want 0", n, err)
	}

	// Not before its time.
	recent := mustCreateFolder(t, s, sm.Folder{Owner: alice, Name: "Recent"})
	if _, err := s.DeleteFolder(ctx, recent.ID); err != nil {
		t.Fatalf("DeleteFolder: %v", err)
	}
	if n, err := s.PurgeFolders(ctx, time.Now().Add(-time.Minute), 10); err != nil || n != 0 {
		t.Errorf("PurgeFolders(a minute ago) = %d, %v; want 0", n, err)
	}
	if _, err := s.PurgeFolders(ctx, later, 0); !errors.Is(err, sm.ErrInvalidQuery) {
		t.Errorf("limit 0: got %v, want ErrInvalidQuery", err)
	}
}

func testDeleteCompletedUploads(t *testing.T, s sm.Store) {
	ctx := t.Context()
	complete := func() sm.Upload {
		u := mustCreateUpload(t, s, newAssetUpload())
		if _, err := s.CompleteUpload(ctx, u.ID, uploaded(u)); err != nil {
			t.Fatalf("CompleteUpload: %v", err)
		}
		return u
	}
	old := complete()
	letTimePass()
	between := time.Now()
	letTimePass()
	recent := complete()
	pending := mustCreateUpload(t, s, newAssetUpload())

	if n, err := s.DeleteCompletedUploads(ctx, between, 10); err != nil || n != 1 {
		t.Fatalf("DeleteCompletedUploads(between) = %d, %v; want 1", n, err)
	}
	if _, err := s.GetUpload(ctx, old.ID); !errors.Is(err, sm.ErrUploadNotFound) {
		t.Errorf("old session after delete = %v, want ErrUploadNotFound", err)
	}
	if _, err := s.GetUpload(ctx, recent.ID); err != nil {
		t.Errorf("recent session deleted too early: %v", err)
	}
	if n, err := s.DeleteCompletedUploads(ctx, time.Now().Add(time.Minute), 10); err != nil || n != 1 {
		t.Errorf("DeleteCompletedUploads(later) = %d, %v; want 1", n, err)
	}
	if _, err := s.GetUpload(ctx, pending.ID); err != nil {
		t.Errorf("pending session deleted: %v", err)
	}
	if _, err := s.DeleteCompletedUploads(ctx, between, 0); !errors.Is(err, sm.ErrInvalidQuery) {
		t.Errorf("limit 0: got %v, want ErrInvalidQuery", err)
	}
}
