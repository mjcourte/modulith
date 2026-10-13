package dam_test

import (
	"errors"
	"maps"
	"slices"
	"testing"

	"github.com/google/uuid"

	"gyoza/services/storage/internal/dam"
)

// Asset uses: which asset versions each project uses (dam/uses.go).

func mustRecordUse(t *testing.T, s dam.Store, projectID string, ref dam.AssetRef) {
	t.Helper()
	if err := s.RecordUse(t.Context(), projectID, ref); err != nil {
		t.Fatalf("RecordUse(%s, %s): %v", projectID, ref, err)
	}
}

func mustListUses(t *testing.T, s dam.Store, projectID string) []dam.AssetRef {
	t.Helper()
	refs, err := s.ListUses(t.Context(), projectID)
	if err != nil {
		t.Fatalf("ListUses(%s): %v", projectID, err)
	}
	return refs
}

func testUsesRecordAndForget(t *testing.T, s dam.Store) {
	ctx := t.Context()
	a := mustCreateAsset(t, s, dam.Asset{Owner: alice}, "image/png")
	addVersions(t, s, a)
	b := mustCreateAsset(t, s, dam.Asset{Owner: alice}, "image/png") // later UUIDv7: sorts after a
	v1, v3, b1 := dam.AssetRef{AssetID: a.ID, Version: 1}, dam.AssetRef{AssetID: a.ID, Version: 3}, dam.AssetRef{AssetID: b.ID, Version: 1}

	// Recorded out of order and twice: one row each, listed by asset then version.
	for _, ref := range []dam.AssetRef{b1, v3, v1, v3} {
		mustRecordUse(t, s, "p1", ref)
	}
	mustRecordUse(t, s, "p2", v1)
	if got, want := mustListUses(t, s, "p1"), []dam.AssetRef{v1, v3, b1}; !slices.Equal(got, want) {
		t.Errorf("ListUses(p1) = %v, want %v", got, want)
	}

	// Forgetting is per project and per version, and repeatable.
	for range 2 {
		if err := s.ForgetUse(ctx, "p1", v1); err != nil {
			t.Fatalf("ForgetUse: %v", err)
		}
	}
	if err := s.ForgetUse(ctx, "p1", dam.AssetRef{AssetID: uuid.NewString(), Version: 1}); err != nil {
		t.Errorf("ForgetUse of a use never recorded: %v", err)
	}
	if got, want := mustListUses(t, s, "p1"), []dam.AssetRef{v3, b1}; !slices.Equal(got, want) {
		t.Errorf("ListUses(p1) after forgetting %s = %v, want %v", v1, got, want)
	}
	if got, want := mustListUses(t, s, "p2"), []dam.AssetRef{v1}; !slices.Equal(got, want) {
		t.Errorf("forgetting p1's use changed p2's: %v, want %v", got, want)
	}
	for _, c := range []struct {
		project string
		ref     dam.AssetRef
		want    bool
	}{{"p1", v1, false}, {"p1", v3, true}, {"p2", v1, true}, {"p2", v3, false}, {"p1", dam.AssetRef{AssetID: "not-a-uuid", Version: 1}, false}} {
		if got, err := s.HasUse(ctx, c.project, c.ref); err != nil || got != c.want {
			t.Errorf("HasUse(%s, %s) = %v, %v; want %v", c.project, c.ref, got, err, c.want)
		}
	}
	if _, err := s.HasUse(ctx, "", v3); !errors.Is(err, dam.ErrInvalidQuery) {
		t.Errorf("HasUse with no project = %v, want ErrInvalidQuery", err)
	}
	if got := mustListUses(t, s, "p-unknown"); len(got) != 0 {
		t.Errorf("ListUses of a project with no uses = %v, want none", got)
	}
}

func testUsesRecordRejects(t *testing.T, s dam.Store) {
	ctx := t.Context()
	a := mustCreateAsset(t, s, dam.Asset{Owner: alice}, "image/png")
	tests := []struct {
		name    string
		project string
		ref     dam.AssetRef
		want    error
	}{
		{"no project", "", dam.AssetRef{AssetID: a.ID, Version: 1}, dam.ErrInvalidQuery},
		{"version 0", "p1", dam.AssetRef{AssetID: a.ID}, dam.ErrInvalidQuery},
		{"no asset ID", "p1", dam.AssetRef{Version: 1}, dam.ErrInvalidQuery},
		{"missing asset", "p1", dam.AssetRef{AssetID: uuid.NewString(), Version: 1}, dam.ErrAssetNotFound},
		{"malformed asset ID", "p1", dam.AssetRef{AssetID: "not-a-uuid", Version: 1}, dam.ErrAssetNotFound},
		{"missing version", "p1", dam.AssetRef{AssetID: a.ID, Version: 2}, dam.ErrVersionNotFound},
	}
	for _, tt := range tests {
		if err := s.RecordUse(ctx, tt.project, tt.ref); !errors.Is(err, tt.want) {
			t.Errorf("%s: RecordUse = %v, want %v", tt.name, err, tt.want)
		}
	}
	if got := mustListUses(t, s, "p1"); len(got) != 0 {
		t.Errorf("rejected uses were recorded: %v", got)
	}
	if err := s.ForgetUse(ctx, "", dam.AssetRef{AssetID: a.ID, Version: 1}); !errors.Is(err, dam.ErrInvalidQuery) {
		t.Errorf("ForgetUse with no project = %v, want ErrInvalidQuery", err)
	}
	if _, err := s.ListUses(ctx, ""); !errors.Is(err, dam.ErrInvalidQuery) {
		t.Errorf("ListUses with no project = %v, want ErrInvalidQuery", err)
	}
	if _, err := s.ForgetProjectUses(ctx, ""); !errors.Is(err, dam.ErrInvalidQuery) {
		t.Errorf("ForgetProjectUses with no project = %v, want ErrInvalidQuery", err)
	}
}

// A project can't start using a trashed asset, but uses recorded before the
// asset was trashed stay (and recording them again still succeeds).
func testUsesOfTrashedAsset(t *testing.T, s dam.Store) {
	ctx := t.Context()
	a := mustCreateAsset(t, s, dam.Asset{Owner: alice}, "image/png")
	ref := dam.AssetRef{AssetID: a.ID, Version: 1}
	mustRecordUse(t, s, "p1", ref)
	if err := s.MarkAssetDeleted(ctx, a.ID); err != nil {
		t.Fatalf("MarkAssetDeleted: %v", err)
	}

	if err := s.RecordUse(ctx, "p2", ref); !errors.Is(err, dam.ErrAssetNotFound) {
		t.Errorf("RecordUse of a trashed asset = %v, want ErrAssetNotFound", err)
	}
	if err := s.RecordUse(ctx, "p1", ref); err != nil {
		t.Errorf("re-recording a use from before the trash: %v", err)
	}
	if got := mustListUses(t, s, "p1"); !slices.Equal(got, []dam.AssetRef{ref}) {
		t.Errorf("trashing the asset changed p1's uses to %v", got)
	}
}

func testUsesCount(t *testing.T, s dam.Store) {
	ctx := t.Context()
	a := mustCreateAsset(t, s, dam.Asset{Owner: alice}, "image/png")
	addVersions(t, s, a)
	b := mustCreateAsset(t, s, dam.Asset{Owner: alice}, "image/png")
	unused := mustCreateAsset(t, s, dam.Asset{Owner: alice}, "image/png")
	// p1 uses two versions of a: it still counts once.
	mustRecordUse(t, s, "p1", dam.AssetRef{AssetID: a.ID, Version: 1})
	mustRecordUse(t, s, "p1", dam.AssetRef{AssetID: a.ID, Version: 2})
	mustRecordUse(t, s, "p2", dam.AssetRef{AssetID: a.ID, Version: 3})
	mustRecordUse(t, s, "p2", dam.AssetRef{AssetID: b.ID, Version: 1})

	got, err := s.CountUses(ctx, []string{a.ID, b.ID, unused.ID, a.ID, "not-a-uuid", uuid.NewString()})
	if err != nil {
		t.Fatalf("CountUses: %v", err)
	}
	if want := map[string]int{a.ID: 2, b.ID: 1}; !maps.Equal(got, want) {
		t.Errorf("CountUses = %v, want %v", got, want)
	}
	if got, err := s.CountUses(ctx, nil); err != nil || len(got) != 0 {
		t.Errorf("CountUses(nil) = %v, %v; want empty", got, err)
	}
}

func testUsesForgetProject(t *testing.T, s dam.Store) {
	ctx := t.Context()
	a := mustCreateAsset(t, s, dam.Asset{Owner: alice}, "image/png")
	addVersions(t, s, a)
	for v := 1; v <= 3; v++ {
		mustRecordUse(t, s, "p1", dam.AssetRef{AssetID: a.ID, Version: v})
	}
	mustRecordUse(t, s, "p2", dam.AssetRef{AssetID: a.ID, Version: 1})

	if n, err := s.ForgetProjectUses(ctx, "p1"); err != nil || n != 3 {
		t.Errorf("ForgetProjectUses = %d, %v; want 3", n, err)
	}
	if n, err := s.ForgetProjectUses(ctx, "p1"); err != nil || n != 0 {
		t.Errorf("ForgetProjectUses again = %d, %v; want 0", n, err)
	}
	if got := mustListUses(t, s, "p1"); len(got) != 0 {
		t.Errorf("p1 still uses %v", got)
	}
	if got := mustListUses(t, s, "p2"); len(got) != 1 {
		t.Errorf("forgetting p1 changed p2's uses to %v", got)
	}
}

// Purging an asset removes its uses: a purged ref resolves to nothing.
func testUsesPurgedWithAsset(t *testing.T, s dam.Store) {
	ctx := t.Context()
	a := mustCreateAsset(t, s, dam.Asset{Owner: alice}, "image/png")
	keep := mustCreateAsset(t, s, dam.Asset{Owner: alice}, "image/png")
	mustRecordUse(t, s, "p1", dam.AssetRef{AssetID: a.ID, Version: 1})
	mustRecordUse(t, s, "p1", dam.AssetRef{AssetID: keep.ID, Version: 1})
	if err := s.MarkAssetDeleted(ctx, a.ID); err != nil {
		t.Fatalf("MarkAssetDeleted: %v", err)
	}
	if err := s.PurgeAsset(ctx, a.ID); err != nil {
		t.Fatalf("PurgeAsset: %v", err)
	}
	if got, want := mustListUses(t, s, "p1"), []dam.AssetRef{{AssetID: keep.ID, Version: 1}}; !slices.Equal(got, want) {
		t.Errorf("ListUses after purging %s = %v, want %v", a.ID, got, want)
	}
	if got, err := s.CountUses(ctx, []string{a.ID}); err != nil || got[a.ID] != 0 {
		t.Errorf("CountUses of a purged asset = %v, %v; want 0", got, err)
	}
}

// SwapUse moves a use to another ref in one step (a fork), or changes nothing.
func testUsesSwap(t *testing.T, s dam.Store) {
	ctx := t.Context()
	a := mustCreateAsset(t, s, dam.Asset{Owner: alice}, "image/png")
	b := mustCreateAsset(t, s, dam.Asset{Owner: alice}, "image/png")
	from, to := dam.AssetRef{AssetID: a.ID, Version: 1}, dam.AssetRef{AssetID: b.ID, Version: 1}
	mustRecordUse(t, s, "p1", from)
	mustRecordUse(t, s, "p2", from)

	for range 2 { // again: nothing changes
		if err := s.SwapUse(ctx, "p1", from, to); err != nil {
			t.Fatalf("SwapUse: %v", err)
		}
	}
	if got := mustListUses(t, s, "p1"); !slices.Equal(got, []dam.AssetRef{to}) {
		t.Errorf("p1 uses %v after the swap, want [%s]", got, to)
	}
	if got := mustListUses(t, s, "p2"); !slices.Equal(got, []dam.AssetRef{from}) {
		t.Errorf("swapping p1 changed p2's uses to %v", got)
	}

	// An invalid target changes nothing: the old use stays.
	missing := dam.AssetRef{AssetID: uuid.NewString(), Version: 1}
	if err := s.SwapUse(ctx, "p2", from, missing); !errors.Is(err, dam.ErrAssetNotFound) {
		t.Errorf("SwapUse to a missing asset = %v, want ErrAssetNotFound", err)
	}
	if err := s.SwapUse(ctx, "p2", from, dam.AssetRef{AssetID: b.ID, Version: 2}); !errors.Is(err, dam.ErrVersionNotFound) {
		t.Errorf("SwapUse to a missing version = %v, want ErrVersionNotFound", err)
	}
	if got := mustListUses(t, s, "p2"); !slices.Equal(got, []dam.AssetRef{from}) {
		t.Errorf("failed swaps changed p2's uses to %v", got)
	}
	// A use that wasn't recorded swaps to a new one.
	if err := s.SwapUse(ctx, "p3", from, to); err != nil {
		t.Fatalf("SwapUse of an unrecorded use: %v", err)
	}
	if got := mustListUses(t, s, "p3"); !slices.Equal(got, []dam.AssetRef{to}) {
		t.Errorf("p3 uses %v, want [%s]", got, to)
	}
	if err := s.SwapUse(ctx, "", from, to); !errors.Is(err, dam.ErrInvalidQuery) {
		t.Errorf("SwapUse with no project = %v, want ErrInvalidQuery", err)
	}
}

// A chosen ID makes creating an asset or folder repeatable: the second call
// returns what the first stored, whatever else it says.
func testChosenIDs(t *testing.T, s dam.Store) {
	ctx := t.Context()
	id := dam.NewID()
	first, err := s.CreateAsset(ctx, dam.Asset{ID: id, Owner: alice, Name: "Logo"}, version("image/png"))
	if err != nil || first.ID != id {
		t.Fatalf("CreateAsset with ID %s = %+v, %v", id, first, err)
	}
	again, err := s.CreateAsset(ctx, dam.Asset{ID: id, Owner: alice, Name: "Other"}, version("image/png"))
	if err != nil || again.ID != id || again.Name != "Logo" {
		t.Errorf("CreateAsset again = %+v, %v; want the first asset unchanged", again, err)
	}
	if vs := mustListVersions(t, s, id); len(vs) != 1 {
		t.Errorf("repeating CreateAsset left %d versions, want 1", len(vs))
	}
	if _, err := s.CreateAsset(ctx, dam.Asset{ID: "not-a-uuid", Owner: alice, Name: "x"}, version("image/png")); !errors.Is(err, dam.ErrInvalidAsset) {
		t.Errorf("CreateAsset with a malformed ID = %v, want ErrInvalidAsset", err)
	}

	fid := dam.NewID()
	f, err := s.CreateFolder(ctx, dam.Folder{ID: fid, Owner: alice, Name: "Brand"})
	if err != nil || f.ID != fid {
		t.Fatalf("CreateFolder with ID = %+v, %v", f, err)
	}
	// Same name too: a repeat must find its folder, not report a clash.
	if g, err := s.CreateFolder(ctx, dam.Folder{ID: fid, Owner: alice, Name: "Brand"}); err != nil || g.ID != fid {
		t.Errorf("CreateFolder again = %+v, %v; want the same folder", g, err)
	}
	if _, err := s.CreateFolder(ctx, dam.Folder{ID: dam.NewID(), Owner: alice, Name: "Brand"}); !errors.Is(err, dam.ErrNameTaken) {
		t.Errorf("another ID, same name = %v, want ErrNameTaken", err)
	}
	if _, err := s.CreateFolder(ctx, dam.Folder{ID: "nope", Owner: alice, Name: "X"}); !errors.Is(err, dam.ErrInvalidFolder) {
		t.Errorf("CreateFolder with a malformed ID = %v, want ErrInvalidFolder", err)
	}
}
