package storagemanager_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"gyoza/services/storageledger"
	sm "gyoza/services/storagemanager"
)

// runStoreTests checks the behaviour every Store implementation must share.
// newStore must return an empty store for each call.
func runStoreTests(t *testing.T, newStore func(t *testing.T) sm.Store) {
	tests := []struct {
		name string
		fn   func(*testing.T, sm.Store)
	}{
		{"CreateThenGet", testCreateThenGet},
		{"GetMissing", testGetAssetMissing},
		{"CreateRejectsInvalid", testCreateRejectsInvalid},
		{"TagsNormalised", testTagsNormalised},
		{"UpdatePartial", testUpdatePartial},
		{"UpdateCanClear", testUpdateCanClear},
		{"UpdateRejectsInvalid", testUpdateRejectsInvalid},
		{"AddVersionBecomesCurrent", testAddVersionBecomesCurrent},
		{"AddVersionRejectsOtherCategory", testAddVersionRejectsOtherCategory},
		{"RevertToOlderVersion", testRevertToOlderVersion},
		{"RevertToMissingVersion", testRevertToMissingVersion},
		{"RevertToCurrentIsNoOp", testRevertToCurrentIsNoOp},
		{"MissingAssetVersions", testMissingAssetVersions},
		{"MarkDeleted", testMarkDeleted},
		{"DeletedAssetsAreReadOnly", testDeletedAssetsAreReadOnly},
		{"ListFilters", testListFilters},
		{"ListPagination", testListPagination},
		{"ListRejectsInvalid", testListRejectsInvalid},
		{"ReturnedSlicesAreCopies", testReturnedSlicesAreCopies},

		// Folders: store_folders_test.go
		{"FolderCreateThenGet", testFolderCreateThenGet},
		{"FolderCreateRejects", testFolderCreateRejects},
		{"FolderNamesUniqueIgnoringCase", testFolderNamesUniqueIgnoringCase},
		{"FolderDepthLimit", testFolderDepthLimit},
		{"ListFolders", testListFolders},
		{"RenameFolder", testRenameFolder},
		{"MoveFolder", testMoveFolder},
		{"MoveFolderDepthCountsSubtree", testMoveFolderDepthCountsSubtree},
		{"AssetsInFolders", testAssetsInFolders},
		{"DeleteEmptyFolder", testDeleteEmptyFolder},
		{"DeleteFolderNotEmpty", testDeleteFolderNotEmpty},
		{"DeleteFolderTree", testDeleteFolderTree},
		{"TrashedFoldersAreReadOnly", testTrashedFoldersAreReadOnly},

		// Upload sessions: store_uploads_test.go
		{"UploadCreateThenGet", testUploadCreateThenGet},
		{"UploadCreateRejects", testUploadCreateRejects},
		{"CompleteUploadNewAsset", testCompleteUploadNewAsset},
		{"CompleteUploadNewVersion", testCompleteUploadNewVersion},
		{"CompleteUploadIsIdempotent", testCompleteUploadIsIdempotent},
		{"CompleteUploadRejects", testCompleteUploadRejects},
		{"ListExpiredUploads", testListExpiredUploads},
		{"DeleteUpload", testDeleteUpload},
		{"UploadSlicesAreCopies", testUploadSlicesAreCopies},
		{"UploadObjectID", testUploadObjectID},

		// Versions by number, breadcrumbs, trash: store_trash_test.go
		{"GetVersion", testGetVersion},
		{"ListOnlyDeleted", testListOnlyDeleted},
		{"RestoreAsset", testRestoreAsset},
		{"FolderPath", testFolderPath},
		{"ListTrashedFolders", testListTrashedFolders},
		{"RestoreDeletion", testRestoreDeletion},
		{"RestoreDeletionResolvesConflicts", testRestoreDeletionResolvesConflicts},

		// Owner lifecycle: store_owners_test.go
		{"TransferOwner", testTransferOwner},
		{"TransferOwnerMidUpload", testTransferOwnerMidUpload},
		{"MarkDeletedByOwner", testMarkDeletedByOwner},
		{"ClearCreatedBy", testClearCreatedByStore},
		{"ExpireUploadsBy", testExpireUploadsBy},

		// Purging for the cleanup job: store_purge_test.go
		{"ListAssetsTrashedBefore", testListAssetsTrashedBefore},
		{"PurgeAsset", testPurgeAsset},
		{"PurgeFolders", testPurgeFolders},
		{"DeleteCompletedUploads", testDeleteCompletedUploads},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.fn(t, newStore(t))
		})
	}
}

var (
	alice   = storageledger.Owner{Kind: storageledger.OwnerUser, ID: "alice"}
	acme    = storageledger.Owner{Kind: storageledger.OwnerOrg, ID: "acme"}
	project = storageledger.Owner{Kind: storageledger.OwnerProject, ID: "p1"}
)

// version returns a valid version of the given type, as the Manager would
// pass after an upload completes.
func version(contentType string) sm.Version {
	return sm.Version{ObjectID: "obj-" + uuid.NewString(), ContentType: contentType, SizeBytes: 1024, CreatedBy: "alice"}
}

// mustCreateAsset creates a with a first version of contentType, or fails the test.
func mustCreateAsset(t *testing.T, s sm.Store, a sm.Asset, contentType string) sm.Asset {
	t.Helper()
	if a.Name == "" {
		a.Name = "asset"
	}
	created, err := s.CreateAsset(t.Context(), a, version(contentType))
	if err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}
	return created
}

func mustGetAsset(t *testing.T, s sm.Store, id string) sm.Asset {
	t.Helper()
	a, err := s.GetAsset(t.Context(), id)
	if err != nil {
		t.Fatalf("GetAsset: %v", err)
	}
	return a
}

func mustListVersions(t *testing.T, s sm.Store, assetID string) []sm.Version {
	t.Helper()
	vs, err := s.ListVersions(t.Context(), assetID)
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	return vs
}

// assetIDs lists the IDs of assets, for readable comparisons.
func assetIDs(assets []sm.Asset) []string {
	out := make([]string, len(assets))
	for i, a := range assets {
		out[i] = a.ID
	}
	return out
}

func ptr[T any](v T) *T { return &v } // for AssetUpdate fields

// letTimePass waits long enough that a new timestamp differs from the last.
// Stores keep microseconds, so two calls in a row can otherwise share a
// timestamp and a test checking "this must not change UpdatedAt" would pass
// even when it did change.
func letTimePass() { time.Sleep(2 * time.Millisecond) }

func testCreateThenGet(t *testing.T, s sm.Store) {
	first := version("image/png")
	created, err := s.CreateAsset(t.Context(), sm.Asset{
		Owner: alice, Name: "Logo", Description: "main logo", Tags: []string{"brand"}, CreatedBy: "alice",
	}, first)
	if err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}

	if u, err := uuid.Parse(created.ID); err != nil || u.Version() != 7 {
		t.Errorf("ID %q is not a UUIDv7", created.ID)
	}
	if created.CurrentVersion != 1 || created.Category != sm.CategoryImage || created.ContentType != "image/png" {
		t.Errorf("created = %+v; want version 1 current, image, image/png", created)
	}
	if created.CreatedAt.IsZero() || !created.UpdatedAt.Equal(created.CreatedAt) || created.DeletedAt != nil {
		t.Errorf("timestamps: created %v updated %v deleted %v", created.CreatedAt, created.UpdatedAt, created.DeletedAt)
	}

	got := mustGetAsset(t, s, created.ID)
	if got.ID != created.ID || got.Name != "Logo" || got.Description != "main logo" ||
		!slices.Equal(got.Tags, []string{"brand"}) || !got.CreatedAt.Equal(created.CreatedAt) {
		t.Errorf("GetAsset = %+v, want %+v", got, created)
	}

	vs := mustListVersions(t, s, created.ID)
	if len(vs) != 1 || vs[0].Number != 1 || vs[0].AssetID != created.ID || vs[0].ObjectID != first.ObjectID || vs[0].CreatedAt.IsZero() {
		t.Errorf("versions = %+v, want one version 1 for object %s", vs, first.ObjectID)
	}
}

func testGetAssetMissing(t *testing.T, s sm.Store) {
	if _, err := s.GetAsset(t.Context(), uuid.NewString()); !errors.Is(err, sm.ErrAssetNotFound) {
		t.Errorf("GetAsset = %v, want ErrAssetNotFound", err)
	}
}

func testCreateRejectsInvalid(t *testing.T, s sm.Store) {
	tests := []struct {
		name  string
		asset sm.Asset
		first sm.Version
	}{
		{"no owner", sm.Asset{Name: "x"}, version("image/png")},
		{"blank name", sm.Asset{Owner: alice, Name: "  "}, version("image/png")},
		{"project owner, other project", sm.Asset{Owner: project, ProjectID: "p2", Name: "x"}, version("image/png")},
		{"version without object", sm.Asset{Owner: alice, Name: "x"}, sm.Version{ContentType: "image/png"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := s.CreateAsset(t.Context(), tt.asset, tt.first); !errors.Is(err, sm.ErrInvalidAsset) {
				t.Errorf("CreateAsset = %v, want ErrInvalidAsset", err)
			}
		})
	}
}

func testTagsNormalised(t *testing.T, s sm.Store) {
	a := mustCreateAsset(t, s, sm.Asset{Owner: alice, Tags: []string{" Logo", "LOGO", "brand"}}, "image/png")
	if want := []string{"brand", "logo"}; !slices.Equal(mustGetAsset(t, s, a.ID).Tags, want) {
		t.Errorf("tags after create = %q, want %q", mustGetAsset(t, s, a.ID).Tags, want)
	}
	if _, err := s.UpdateAsset(t.Context(), a.ID, sm.AssetUpdate{Tags: ptr([]string{"Hero ", "hero"})}); err != nil {
		t.Fatalf("UpdateAsset: %v", err)
	}
	if want := []string{"hero"}; !slices.Equal(mustGetAsset(t, s, a.ID).Tags, want) {
		t.Errorf("tags after update = %q, want %q", mustGetAsset(t, s, a.ID).Tags, want)
	}
}

func testUpdatePartial(t *testing.T, s sm.Store) {
	a := mustCreateAsset(t, s, sm.Asset{Owner: alice, Name: "old", Description: "keep me", Tags: []string{"keep"}}, "image/png")

	updated, err := s.UpdateAsset(t.Context(), a.ID, sm.AssetUpdate{Name: ptr("  new  ")})
	if err != nil {
		t.Fatalf("UpdateAsset: %v", err)
	}
	if updated.Name != "new" || updated.Description != "keep me" || !slices.Equal(updated.Tags, []string{"keep"}) {
		t.Errorf("updated = %+v; want only the (trimmed) name changed", updated)
	}
	if updated.UpdatedAt.Before(a.UpdatedAt) || !updated.CreatedAt.Equal(a.CreatedAt) {
		t.Errorf("UpdatedAt %v went back, or CreatedAt %v changed", updated.UpdatedAt, updated.CreatedAt)
	}
	if got := mustGetAsset(t, s, a.ID); got.Name != "new" {
		t.Errorf("GetAsset after update: name %q, want new", got.Name)
	}
}

// A pointer to an empty value clears a field; nil leaves it alone.
func testUpdateCanClear(t *testing.T, s sm.Store) {
	a := mustCreateAsset(t, s, sm.Asset{Owner: alice, Description: "text", Tags: []string{"x"}}, "image/png")
	updated, err := s.UpdateAsset(t.Context(), a.ID, sm.AssetUpdate{Description: ptr(""), Tags: ptr([]string{})})
	if err != nil {
		t.Fatalf("UpdateAsset: %v", err)
	}
	if updated.Description != "" || len(updated.Tags) != 0 {
		t.Errorf("updated = %+v; want description and tags cleared", updated)
	}
}

func testUpdateRejectsInvalid(t *testing.T, s sm.Store) {
	a := mustCreateAsset(t, s, sm.Asset{Owner: alice, Name: "keep"}, "image/png")
	if _, err := s.UpdateAsset(t.Context(), a.ID, sm.AssetUpdate{Name: ptr(" ")}); !errors.Is(err, sm.ErrInvalidAsset) {
		t.Errorf("UpdateAsset with blank name = %v, want ErrInvalidAsset", err)
	}
	if got := mustGetAsset(t, s, a.ID); got.Name != "keep" {
		t.Errorf("rejected update changed the name to %q", got.Name)
	}
	if _, err := s.UpdateAsset(t.Context(), uuid.NewString(), sm.AssetUpdate{Name: ptr("x")}); !errors.Is(err, sm.ErrAssetNotFound) {
		t.Errorf("UpdateAsset of missing asset = %v, want ErrAssetNotFound", err)
	}
}

func testAddVersionBecomesCurrent(t *testing.T, s sm.Store) {
	a := mustCreateAsset(t, s, sm.Asset{Owner: alice}, "image/png")
	for i, contentType := range []string{"image/jpeg", "image/webp"} {
		v, err := s.AddVersion(t.Context(), a.ID, version(contentType))
		if err != nil {
			t.Fatalf("AddVersion: %v", err)
		}
		if want := i + 2; v.Number != want || v.AssetID != a.ID || v.CreatedAt.IsZero() {
			t.Errorf("AddVersion = %+v, want number %d of asset %s", v, want, a.ID)
		}
		got := mustGetAsset(t, s, a.ID)
		if got.CurrentVersion != v.Number || got.ContentType != contentType {
			t.Errorf("asset current %d (%s), want %d (%s)", got.CurrentVersion, got.ContentType, v.Number, contentType)
		}
	}
	if vs := mustListVersions(t, s, a.ID); len(vs) != 3 || vs[0].Number != 1 || vs[2].Number != 3 {
		t.Errorf("versions = %+v, want 1, 2, 3 in order", vs)
	}
}

func testAddVersionRejectsOtherCategory(t *testing.T, s sm.Store) {
	a := mustCreateAsset(t, s, sm.Asset{Owner: alice}, "image/png")
	if _, err := s.AddVersion(t.Context(), a.ID, version("video/mp4")); !errors.Is(err, sm.ErrInvalidAsset) {
		t.Errorf("AddVersion(video) to an image = %v, want ErrInvalidAsset", err)
	}
	if vs := mustListVersions(t, s, a.ID); len(vs) != 1 {
		t.Errorf("rejected version was stored: %d versions", len(vs))
	}
	if got := mustGetAsset(t, s, a.ID); got.CurrentVersion != 1 || got.ContentType != "image/png" {
		t.Errorf("rejected version changed the asset: %+v", got)
	}
}

// addVersions gives a an extra jpeg and webp version: 3 versions, 3 current.
func addVersions(t *testing.T, s sm.Store, a sm.Asset) {
	t.Helper()
	for _, contentType := range []string{"image/jpeg", "image/webp"} {
		if _, err := s.AddVersion(t.Context(), a.ID, version(contentType)); err != nil {
			t.Fatalf("AddVersion: %v", err)
		}
	}
}

func testRevertToOlderVersion(t *testing.T, s sm.Store) {
	a := mustCreateAsset(t, s, sm.Asset{Owner: alice}, "image/png")
	addVersions(t, s, a)

	reverted, err := s.SetCurrentVersion(t.Context(), a.ID, 1)
	if err != nil {
		t.Fatalf("SetCurrentVersion: %v", err)
	}
	if reverted.CurrentVersion != 1 || reverted.ContentType != "image/png" {
		t.Errorf("reverted = %+v, want version 1, image/png", reverted)
	}
	// Reverting only moves the pointer: no version is added or removed.
	if vs := mustListVersions(t, s, a.ID); len(vs) != 3 {
		t.Errorf("revert changed the version list to %d versions", len(vs))
	}
	if got := mustGetAsset(t, s, a.ID); got.CurrentVersion != 1 {
		t.Errorf("GetAsset after revert: current %d, want 1", got.CurrentVersion)
	}
}

func testRevertToMissingVersion(t *testing.T, s sm.Store) {
	a := mustCreateAsset(t, s, sm.Asset{Owner: alice}, "image/png")
	for _, n := range []int{0, -1, 2} {
		if _, err := s.SetCurrentVersion(t.Context(), a.ID, n); !errors.Is(err, sm.ErrVersionNotFound) {
			t.Errorf("SetCurrentVersion(%d) = %v, want ErrVersionNotFound", n, err)
		}
	}
}

func testRevertToCurrentIsNoOp(t *testing.T, s sm.Store) {
	a := mustCreateAsset(t, s, sm.Asset{Owner: alice}, "image/png")
	letTimePass()
	got, err := s.SetCurrentVersion(t.Context(), a.ID, 1)
	if err != nil {
		t.Fatalf("SetCurrentVersion: %v", err)
	}
	if !got.UpdatedAt.Equal(a.UpdatedAt) {
		t.Errorf("setting the current version moved UpdatedAt from %v to %v", a.UpdatedAt, got.UpdatedAt)
	}
}

func testMissingAssetVersions(t *testing.T, s sm.Store) {
	missing := uuid.NewString()
	if _, err := s.ListVersions(t.Context(), missing); !errors.Is(err, sm.ErrAssetNotFound) {
		t.Errorf("ListVersions = %v, want ErrAssetNotFound", err)
	}
	if _, err := s.AddVersion(t.Context(), missing, version("image/png")); !errors.Is(err, sm.ErrAssetNotFound) {
		t.Errorf("AddVersion = %v, want ErrAssetNotFound", err)
	}
	if _, err := s.SetCurrentVersion(t.Context(), missing, 1); !errors.Is(err, sm.ErrAssetNotFound) {
		t.Errorf("SetCurrentVersion = %v, want ErrAssetNotFound", err)
	}
}

func testMarkDeleted(t *testing.T, s sm.Store) {
	ctx := t.Context()
	a := mustCreateAsset(t, s, sm.Asset{Owner: alice}, "image/png")
	if err := s.MarkAssetDeleted(ctx, a.ID); err != nil {
		t.Fatalf("MarkAssetDeleted: %v", err)
	}
	first := mustGetAsset(t, s, a.ID).DeletedAt
	if first == nil {
		t.Fatal("DeletedAt not set")
	}
	letTimePass()
	if err := s.MarkAssetDeleted(ctx, a.ID); err != nil {
		t.Errorf("second MarkAssetDeleted: %v", err)
	}
	if again := mustGetAsset(t, s, a.ID).DeletedAt; again == nil || !again.Equal(*first) {
		t.Errorf("second delete changed DeletedAt from %v to %v", *first, again)
	}
	if err := s.MarkAssetDeleted(ctx, uuid.NewString()); !errors.Is(err, sm.ErrAssetNotFound) {
		t.Errorf("MarkAssetDeleted of missing asset = %v, want ErrAssetNotFound", err)
	}
}

// Deleted assets stay readable but can't be changed.
func testDeletedAssetsAreReadOnly(t *testing.T, s sm.Store) {
	ctx := t.Context()
	a := mustCreateAsset(t, s, sm.Asset{Owner: alice}, "image/png")
	addVersions(t, s, a)
	if err := s.MarkAssetDeleted(ctx, a.ID); err != nil {
		t.Fatalf("MarkAssetDeleted: %v", err)
	}

	if got := mustGetAsset(t, s, a.ID); got.DeletedAt == nil {
		t.Error("GetAsset of a deleted asset: DeletedAt not set")
	}
	if vs := mustListVersions(t, s, a.ID); len(vs) != 3 {
		t.Errorf("ListVersions of a deleted asset = %d versions, want 3", len(vs))
	}
	if _, err := s.UpdateAsset(ctx, a.ID, sm.AssetUpdate{Name: ptr("x")}); !errors.Is(err, sm.ErrAssetNotFound) {
		t.Errorf("UpdateAsset = %v, want ErrAssetNotFound", err)
	}
	if _, err := s.AddVersion(ctx, a.ID, version("image/png")); !errors.Is(err, sm.ErrAssetNotFound) {
		t.Errorf("AddVersion = %v, want ErrAssetNotFound", err)
	}
	if _, err := s.SetCurrentVersion(ctx, a.ID, 1); !errors.Is(err, sm.ErrAssetNotFound) {
		t.Errorf("SetCurrentVersion = %v, want ErrAssetNotFound", err)
	}
}

func testListFilters(t *testing.T, s sm.Store) {
	logo := mustCreateAsset(t, s, sm.Asset{Owner: acme, ProjectID: "p1", Tags: []string{"brand"}}, "image/png")
	intro := mustCreateAsset(t, s, sm.Asset{Owner: acme, ProjectID: "p1", Tags: []string{"brand", "intro"}}, "video/mp4")
	// In scope for most queries but without the brand tag, so tag filters must actually filter.
	plain := mustCreateAsset(t, s, sm.Asset{Owner: acme, ProjectID: "p1", Tags: []string{"other"}}, "image/png")
	snapshotLike := mustCreateAsset(t, s, sm.Asset{Owner: project}, "application/json") // project-owned: ProjectID filled in
	personal := mustCreateAsset(t, s, sm.Asset{Owner: alice, Tags: []string{"brand"}}, "image/jpeg")
	deleted := mustCreateAsset(t, s, sm.Asset{Owner: acme, ProjectID: "p1", Tags: []string{"brand"}}, "image/png")
	if err := s.MarkAssetDeleted(t.Context(), deleted.ID); err != nil {
		t.Fatalf("MarkAssetDeleted: %v", err)
	}

	tests := []struct {
		name string
		q    sm.AssetQuery
		want []sm.Asset
	}{
		{"by project", sm.AssetQuery{ProjectID: "p1"}, []sm.Asset{logo, intro, plain, snapshotLike}},
		{"by owner", sm.AssetQuery{Owner: acme}, []sm.Asset{logo, intro, plain}},
		{"by tag, normalised", sm.AssetQuery{Owner: acme, Tag: " BRAND "}, []sm.Asset{logo, intro}},
		{"by rarer tag", sm.AssetQuery{ProjectID: "p1", Tag: "intro"}, []sm.Asset{intro}},
		{"by category", sm.AssetQuery{ProjectID: "p1", Category: sm.CategoryVideo}, []sm.Asset{intro}},
		{"tag and category", sm.AssetQuery{ProjectID: "p1", Tag: "brand", Category: sm.CategoryImage}, []sm.Asset{logo}},
		{"other owner", sm.AssetQuery{Owner: alice}, []sm.Asset{personal}},
		{"including deleted", sm.AssetQuery{Owner: acme, IncludeDeleted: true}, []sm.Asset{logo, intro, plain, deleted}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, err := s.ListAssets(t.Context(), tt.q)
			if err != nil {
				t.Fatalf("ListAssets: %v", err)
			}
			// UUIDv7 IDs make "oldest first" exact, so order is checked too.
			if got, want := assetIDs(page.Assets), assetIDs(tt.want); !slices.Equal(got, want) {
				t.Errorf("got %v, want %v", got, want)
			}
		})
	}
}

func testListPagination(t *testing.T, s sm.Store) {
	var want []string
	for range 7 {
		want = append(want, mustCreateAsset(t, s, sm.Asset{Owner: acme}, "image/png").ID)
	}

	var got []string
	var sizes []int
	q := sm.AssetQuery{Owner: acme, PageSize: 3}
	for {
		page, err := s.ListAssets(t.Context(), q)
		if err != nil {
			t.Fatalf("ListAssets: %v", err)
		}
		got = append(got, assetIDs(page.Assets)...)
		sizes = append(sizes, len(page.Assets))
		if page.NextPageToken == "" {
			break
		}
		q.PageToken = page.NextPageToken
	}
	if !slices.Equal(sizes, []int{3, 3, 1}) {
		t.Errorf("page sizes = %v, want [3 3 1]", sizes)
	}
	if !slices.Equal(got, want) {
		t.Errorf("paged through %v, want %v in creation order", got, want)
	}
}

func testListRejectsInvalid(t *testing.T, s sm.Store) {
	tests := map[string]sm.AssetQuery{
		"no scope":         {},
		"two scopes":       {ProjectID: "p1", Owner: acme},
		"unknown category": {ProjectID: "p1", Category: "spreadsheet"},
		"negative size":    {ProjectID: "p1", PageSize: -1},
		"garbage token":    {ProjectID: "p1", PageToken: "!!"},
		"token not an ID":  {ProjectID: "p1", PageToken: "bm90LWEtdXVpZA"},
		"filter, no scope": {Tag: "brand"},
		"owner without ID": {Owner: storageledger.Owner{Kind: storageledger.OwnerUser}},
	}
	for name, q := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := s.ListAssets(t.Context(), q); !errors.Is(err, sm.ErrInvalidQuery) {
				t.Errorf("ListAssets = %v, want ErrInvalidQuery", err)
			}
		})
	}
}

// Slices handed in or out must not share memory with what's stored.
func testReturnedSlicesAreCopies(t *testing.T, s sm.Store) {
	ctx := t.Context()
	tags := []string{"a", "b"}
	a := mustCreateAsset(t, s, sm.Asset{Owner: alice, Tags: tags}, "image/png")
	tags[0] = "changed-input"    // the caller reusing its slice
	a.Tags[0] = "changed-create" // changing what CreateAsset returned

	got := mustGetAsset(t, s, a.ID)
	got.Tags[0] = "changed-get"

	page, err := s.ListAssets(ctx, sm.AssetQuery{Owner: alice})
	if err != nil {
		t.Fatalf("ListAssets: %v", err)
	}
	page.Assets[0].Tags[0] = "changed-list"

	vs := mustListVersions(t, s, a.ID)
	vs[0].ObjectID = "changed-versions"

	if final := mustGetAsset(t, s, a.ID); !slices.Equal(final.Tags, []string{"a", "b"}) {
		t.Errorf("stored tags changed to %q", final.Tags)
	}
	if final := mustListVersions(t, s, a.ID); final[0].ObjectID == "changed-versions" {
		t.Error("stored version changed through a returned slice")
	}
}
