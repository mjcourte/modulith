package manager_test

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"testing"

	"gyoza/internal/engine"
	"gyoza/services/documents/internal/manager"
	"gyoza/services/storage"
)

// storedAssets loads a project's latest stored snapshot and returns the
// asset refs it uses, sorted.
func storedAssets(t *testing.T, w *world, projectID string) ([]engine.AssetRef, map[string]any) {
	t.Helper()
	ctx := context.Background()
	ref, err := w.store.Latest(ctx, projectID)
	if err != nil {
		t.Fatalf("Latest(%s): %v", projectID, err)
	}
	snap, err := w.store.Get(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := engine.Mock{}.New(ctx, engine.Settings{ID: "check"})
	if err := doc.Merge(ctx, snap); err != nil {
		t.Fatal(err)
	}
	refs, err := doc.Assets(ctx, engine.AssetFilter{})
	if err != nil {
		t.Fatal(err)
	}
	slices.SortFunc(refs, compareRefs)
	return refs, registers(t, manager.Document{Snapshot: snap})
}

func compareRefs(a, b engine.AssetRef) int {
	return cmp.Or(cmp.Compare(a.ID, b.ID), cmp.Compare(a.Version, b.Version))
}

func TestCloneDocument(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	x1, x2 := engine.AssetRef{ID: "x", Version: 1}, engine.AssetRef{ID: "x", Version: 2}
	copyOf := func(r engine.AssetRef) engine.AssetRef {
		return engine.AssetRef{ID: storage.CopyAssetID("q", storage.Owner{Kind: storage.OwnerProject, ID: "q"}, storage.AssetRef{AssetID: r.ID, Version: r.Version}), Version: 1}
	}
	// Not saved yet: the clone must copy the open state, not the stored one.
	w.mustEdit("p", engine.MockSet{Key: "title", Value: "hello"}, engine.MockSet{Key: "bg", Value: x1},
		engine.MockSet{Key: "logo", Value: x1}, engine.MockSet{Key: "photo", Value: x2})
	calls := w.uses.calls

	if err := w.m.CloneDocument(ctx, "mallory", "p", "q"); !errors.Is(err, manager.ErrForbidden) {
		t.Errorf("clone by a stranger: got %v, want ErrForbidden", err)
	}
	// Reading the source is enough: the target is iam's to vouch for.
	if err := w.m.CloneDocument(ctx, "rita", "p", "q"); err != nil {
		t.Fatalf("CloneDocument: %v", err)
	}
	refs, regs := storedAssets(t, w, "q")
	want := []engine.AssetRef{copyOf(x1), copyOf(x2)}
	slices.SortFunc(want, compareRefs)
	if !slices.Equal(refs, want) {
		t.Errorf("clone uses %v, want the copies %v", refs, want)
	}
	if regs["title"] != "hello" {
		t.Errorf("clone's title = %v, want hello", regs["title"])
	}
	if w.uses.calls != calls {
		t.Errorf("cloning made %d usage calls; storage's copy records the clone's uses", w.uses.calls-calls)
	}
	d, err := w.m.GetDocument(ctx, "ann", "p")
	if err != nil {
		t.Fatal(err)
	}
	if regs := registers(t, d); regs["title"] != "hello" {
		t.Errorf("source after cloning: %v", regs)
	}
	if got := w.uses.of("p"); !slices.Equal(got, []engine.AssetRef{x1, x2}) {
		t.Errorf("source's uses = %v, want unchanged", got)
	}

	// A resumed clone job repeats the call: nothing new is stored, even if
	// the source changed since.
	first, _ := w.store.Latest(ctx, "q")
	w.mustEdit("p", engine.MockSet{Key: "title", Value: "changed"})
	if err := w.m.CloneDocument(ctx, "ann", "p", "q"); err != nil {
		t.Fatalf("CloneDocument again: %v", err)
	}
	if again, _ := w.store.Latest(ctx, "q"); again != first {
		t.Errorf("repeating the clone stored %+v over %+v", again, first)
	}
}

func TestCloneDocumentOfUnopenedSource(t *testing.T) {
	w := newWorld(t)
	if err := w.m.CloneDocument(context.Background(), "ann", "fresh", "q"); err != nil {
		t.Fatalf("CloneDocument: %v", err)
	}
	if refs, regs := storedAssets(t, w, "q"); len(refs) != 0 || len(regs) != 0 {
		t.Errorf("clone of an empty document: %v, %v", refs, regs)
	}
}

func TestReplaceAssetRef(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	x1, y1, fork := engine.AssetRef{ID: "x", Version: 1}, engine.AssetRef{ID: "y", Version: 1}, engine.AssetRef{ID: "fork", Version: 1}
	w.mustEdit("p", engine.MockSet{Key: "bg", Value: x1}, engine.MockSet{Key: "logo", Value: x1}, engine.MockSet{Key: "photo", Value: y1})

	if _, err := w.m.ReplaceAssetRef(ctx, "rita", "p", x1, fork); !errors.Is(err, manager.ErrForbidden) {
		t.Errorf("by a reader: got %v, want ErrForbidden", err)
	}
	out, err := w.m.ReplaceAssetRef(ctx, "ann", "p", x1, fork)
	if err != nil || len(out.Payload) == 0 {
		t.Fatalf("ReplaceAssetRef: %d bytes, %v", len(out.Payload), err)
	}
	// The uses follow the document's assets: x@1 is gone from both registers.
	if got := w.uses.of("p"); !slices.Equal(got, []engine.AssetRef{fork, y1}) {
		t.Errorf("uses = %v, want [fork@1 y@1]", got)
	}
	none, err := w.m.ReplaceAssetRef(ctx, "ann", "p", x1, fork)
	if err != nil || len(none.Payload) != 0 {
		t.Errorf("replacing an unused ref: %d bytes, %v; want an empty delta", len(none.Payload), err)
	}
}
