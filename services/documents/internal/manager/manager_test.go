package manager_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"gyoza/internal/engine"
	"gyoza/services/documents/internal/manager"
	"gyoza/services/documents/internal/snapshots"
	"gyoza/services/documents/internal/wal"
	"gyoza/services/storage"
)

func TestGetDocumentCreatesOnFirstOpen(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	d, err := w.m.GetDocument(ctx, "rita", "p")
	if err != nil {
		t.Fatal(err)
	}
	if regs := registers(t, d); len(regs) != 0 {
		t.Errorf("new document has registers %v", regs)
	}
	ref, err := w.store.Latest(ctx, "p")
	if err != nil || ref.Version != 1 {
		t.Fatalf("stored on first open: %+v, %v; want version 1", ref, err)
	}
	if _, err := w.m.GetDocument(ctx, "rita", "p"); err != nil {
		t.Fatal(err)
	}
	if ref, _ := w.store.Latest(ctx, "p"); ref.Version != 1 {
		t.Errorf("a second open stored again: latest is version %d", ref.Version)
	}
}

func TestInitDocument(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	if _, err := w.m.InitDocument(ctx, "rita", "p"); !errors.Is(err, manager.ErrForbidden) {
		t.Errorf("init by a reader: got %v, want ErrForbidden", err)
	}
	if _, err := w.m.InitDocument(ctx, "ann", "p"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.m.InitDocument(ctx, "ann", "p"); !errors.Is(err, manager.ErrExists) {
		t.Errorf("second init: got %v, want ErrExists", err)
	}
	if _, err := w.m.GetDocument(ctx, "ann", "q"); err != nil { // created by opening
		t.Fatal(err)
	}
	if _, err := w.m.InitDocument(ctx, "ann", "q"); !errors.Is(err, manager.ErrExists) {
		t.Errorf("init after open: got %v, want ErrExists", err)
	}
}

func TestUpdateThenGetAndRestart(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	out := w.mustEdit("p", engine.MockSet{Key: "title", Value: "Poster"})
	if out.Format != engine.WireNative || len(out.Payload) == 0 {
		t.Fatalf("returned delta %s with %d bytes, want a native delta", out.Format, len(out.Payload))
	}

	d, err := w.m.GetDocument(ctx, "rita", "p")
	if err != nil {
		t.Fatal(err)
	}
	if got := registers(t, d)["title"]; got != "Poster" {
		t.Errorf("title = %v, want Poster", got)
	}

	w.restart() // Close saves the unsaved edit; the new Manager loads it
	d, err = w.m.GetDocument(ctx, "rita", "p")
	if err != nil {
		t.Fatal(err)
	}
	if got := registers(t, d)["title"]; got != "Poster" {
		t.Errorf("after restart: title = %v, want Poster", got)
	}
}

func TestQueryDocument(t *testing.T) {
	w := newWorld(t)
	w.mustEdit("p", engine.MockSet{Key: "w", Value: 1080})
	v, err := w.m.QueryDocument(context.Background(), "rita", "p", engine.Query{Format: engine.QueryGraphQL})
	if err != nil {
		t.Fatal(err)
	}
	var view struct{ Registers map[string]any }
	if err := json.Unmarshal(v.Data, &view); err != nil || view.Registers["w"] != float64(1080) {
		t.Errorf("view = %s, %v; want w=1080", v.Data, err)
	}
}

func TestDocumentPermissions(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.mustEdit("p", engine.MockSet{Key: "k", Value: "by ann"})

	if _, err := w.edit("rita", "p", engine.MockSet{Key: "k", Value: "by rita"}); !errors.Is(err, manager.ErrForbidden) {
		t.Errorf("edit by a reader: got %v, want ErrForbidden", err)
	}
	if _, err := w.m.GetDocument(ctx, "mallory", "p"); !errors.Is(err, manager.ErrForbidden) {
		t.Errorf("get by a stranger: got %v, want ErrForbidden", err)
	}
	if _, err := w.m.QueryDocument(ctx, "mallory", "p", engine.Query{Format: engine.QueryNative}); !errors.Is(err, manager.ErrForbidden) {
		t.Errorf("query by a stranger: got %v, want ErrForbidden", err)
	}
	d, err := w.m.GetDocument(ctx, "rita", "p")
	if err != nil {
		t.Fatal(err)
	}
	if got := registers(t, d)["k"]; got != "by ann" {
		t.Errorf("k = %v after a forbidden edit, want \"by ann\"", got)
	}
}

func TestUpdateRejectsWrongFormat(t *testing.T) {
	w := newWorld(t)
	in := engine.DeltaInput{Format: engine.WireJSON, Version: 1, Payload: []byte(`{}`)}
	if _, err := w.m.UpdateDocument(context.Background(), "ann", "p", in); !errors.Is(err, engine.ErrUnsupportedFormat) {
		t.Errorf("got %v, want engine.ErrUnsupportedFormat", err)
	}
}

func TestUpdateKeepsAssetUses(t *testing.T) {
	w := newWorld(t)
	x1, x2, y1 := engine.AssetRef{ID: "x", Version: 1}, engine.AssetRef{ID: "x", Version: 2}, engine.AssetRef{ID: "y", Version: 1}
	want := func(when string, refs ...engine.AssetRef) {
		t.Helper()
		if got := w.uses.of("p"); !slices.Equal(got, refs) {
			t.Errorf("%s: uses = %v, want %v", when, got, refs)
		}
	}

	w.mustEdit("p", engine.MockSet{Key: "bg", Value: x1}, engine.MockSet{Key: "photo", Value: y1})
	want("after adding x@1 and y@1", x1, y1)
	w.mustEdit("p", engine.MockSet{Key: "logo", Value: x1})
	want("after a second use of x@1", x1, y1)
	w.mustEdit("p", engine.MockSet{Key: "bg", Removed: true})
	want("after removing one of two uses of x@1", x1, y1)
	w.mustEdit("p", engine.MockSet{Key: "logo", Value: x2}, engine.MockSet{Key: "photo", Removed: true})
	want("after moving to x@2 and dropping y", x2)

	calls := w.uses.calls
	w.mustEdit("p", engine.MockSet{Key: "title", Value: "no assets"})
	if w.uses.calls != calls {
		t.Errorf("an edit without asset changes made %d storage calls", w.uses.calls-calls)
	}
}

func TestUpdateRetriesFailedAssetUses(t *testing.T) {
	w := newWorld(t)
	x1 := engine.AssetRef{ID: "x", Version: 1}
	w.uses.fail = true
	out, err := w.edit("ann", "p", engine.MockSet{Key: "bg", Value: x1})
	if !errors.Is(err, manager.ErrUsesNotRecorded) || len(out.Payload) == 0 {
		t.Fatalf("got a %d-byte delta and %v; want the applied delta with ErrUsesNotRecorded", len(out.Payload), err)
	}
	w.uses.fail = false
	w.mustEdit("p", engine.MockSet{Key: "title", Value: "unrelated"})
	if got := w.uses.of("p"); !slices.Equal(got, []engine.AssetRef{x1}) {
		t.Errorf("uses = %v after storage recovered, want [x@1]", got)
	}
}

// A record that failed before a restart is made by the first edit after it.
func TestUpdateHealsAssetUsesAfterRestart(t *testing.T) {
	w := newWorld(t)
	x1 := engine.AssetRef{ID: "x", Version: 1}
	w.uses.fail = true
	if _, err := w.edit("ann", "p", engine.MockSet{Key: "bg", Value: x1}); !errors.Is(err, manager.ErrUsesNotRecorded) {
		t.Fatalf("got %v, want ErrUsesNotRecorded", err)
	}
	w.uses.fail = false
	w.restart()
	w.mustEdit("p", engine.MockSet{Key: "title", Value: "unrelated"})
	if got := w.uses.of("p"); !slices.Equal(got, []engine.AssetRef{x1}) {
		t.Errorf("uses = %v after a restart, want [x@1]", got)
	}
}

// Another replica stored a snapshot covering a later stream sequence while
// this one had unsaved edits. Saving must not make the older state Latest:
// it merges the newer snapshot and stores the merge at the newer sequence.
func TestSaveNeverGoesBackInTime(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.mustEdit("p", engine.MockSet{Key: "mine", Value: "unsaved here"})

	other, _ := engine.Mock{}.New(ctx, engine.Settings{ID: "p", Replica: "other"})
	in, _ := engine.MockDelta("", 0, engine.MockSet{Key: "theirs", Value: "stored elsewhere"})
	if _, err := other.Edit(ctx, in); err != nil {
		t.Fatal(err)
	}
	theirs, _ := other.Snapshot(ctx)
	if _, err := w.store.Put(ctx, "p", theirs, 50, snapshots.Edit{}); err != nil {
		t.Fatal(err)
	}

	w.restart() // Close saves this replica's state
	ref, err := w.store.Latest(ctx, "p")
	if err != nil || ref.Seq != 50 {
		t.Fatalf("latest %+v, %v; want a snapshot covering seq 50", ref, err)
	}
	d, err := w.m.GetDocument(ctx, "rita", "p")
	if err != nil {
		t.Fatal(err)
	}
	regs := registers(t, d)
	if regs["mine"] != "unsaved here" || regs["theirs"] != "stored elsewhere" || d.Seq != 50 {
		t.Errorf("after restart: %v at seq %d; want both edits at seq 50", regs, d.Seq)
	}
}

// A delta stamped in the future is refused from the API and dropped from the
// stream (ErrBadDelta terminates it), and the document is unchanged.
func TestForgedStampsAreRefused(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.mustEdit("p", engine.MockSet{Key: "k", Value: "honest"})
	future := engine.HLC(time.Now().Add(time.Hour))
	forged, err := engine.MockDelta("attacker", future, engine.MockSet{Key: "k", Value: "forged"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.m.UpdateDocument(ctx, "ann", "p", forged); !errors.Is(err, engine.ErrBadStamp) {
		t.Errorf("UpdateDocument: got %v, want engine.ErrBadStamp", err)
	}
	if err := w.m.ApplyDelta(ctx, wal.Delta{ProjectID: "p", Seq: 7, ActorID: "ann", Delta: forged}); !errors.Is(err, wal.ErrBadDelta) {
		t.Errorf("ApplyDelta: got %v, want wal.ErrBadDelta (dropped, not redelivered)", err)
	}
	d, _ := w.m.GetDocument(ctx, "rita", "p")
	if got := registers(t, d)["k"]; got != "honest" {
		t.Errorf("k = %v, want the forged writes refused", got)
	}
}

// A delta for a project storage doesn't know can never apply: ApplyDelta
// marks it bad, so the consumer drops it rather than retrying it forever.
func TestApplyDeltaForUnknownProjectIsDropped(t *testing.T) {
	st, err := storage.NewMemory(storage.AllowAll{}, storage.Config{Bucket: "test"}) // knows no projects
	if err != nil {
		t.Fatal(err)
	}
	store := snapshots.NewStorageStore(st, nil) // never reaches a transfer: Begin refuses first
	m := manager.New(engine.Mock{}, store, fakeAuthz{}, &fakeUses{rows: map[string][]engine.AssetRef{}}, manager.Config{Replica: "server"})
	t.Cleanup(func() { m.Close(context.Background()) })
	in, _ := engine.MockDelta("", 0, engine.MockSet{Key: "k", Value: "v"})
	if err := m.ApplyDelta(context.Background(), wal.Delta{ProjectID: "gone", Seq: 1, ActorID: "ann", Delta: in}); !errors.Is(err, wal.ErrBadDelta) {
		t.Errorf("got %v, want wal.ErrBadDelta", err)
	}
}

func TestRevertDocument(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	x1, x2 := engine.AssetRef{ID: "x", Version: 1}, engine.AssetRef{ID: "x", Version: 2}
	w.mustEdit("p", engine.MockSet{Key: "title", Value: "good"}, engine.MockSet{Key: "img", Value: x1})
	w.restart() // saves version 2 (version 1 is the empty first snapshot)
	w.mustEdit("p", engine.MockSet{Key: "title", Value: "stuck"}, engine.MockSet{Key: "img", Value: x2}, engine.MockSet{Key: "junk", Value: "x"})

	if _, err := w.m.RevertDocument(ctx, "rita", "p", 2); !errors.Is(err, manager.ErrForbidden) {
		t.Errorf("revert by a reader: got %v, want ErrForbidden", err)
	}
	out, err := w.m.RevertDocument(ctx, "ann", "p", 2)
	if err != nil || len(out.Payload) == 0 {
		t.Fatalf("RevertDocument: %d bytes, %v", len(out.Payload), err)
	}
	d, _ := w.m.GetDocument(ctx, "rita", "p")
	regs := registers(t, d)
	if regs["title"] != "good" || regs["junk"] != nil {
		t.Errorf("after revert: %v, want title good and no junk", regs)
	}
	if got := w.uses.of("p"); !slices.Equal(got, []engine.AssetRef{x1}) {
		t.Errorf("uses after revert = %v, want [x@1] (x@2 forgotten)", got)
	}

	again, err := w.m.RevertDocument(ctx, "ann", "p", 2)
	if err != nil || len(again.Payload) != 0 {
		t.Errorf("reverting to the current state: %d bytes, %v; want an empty delta", len(again.Payload), err)
	}
	if _, err := w.m.RevertDocument(ctx, "ann", "p", 99); !errors.Is(err, snapshots.ErrNotFound) {
		t.Errorf("unknown version: got %v, want snapshots.ErrNotFound", err)
	}
}

// Another server replica whose clock runs ahead past MaxSkew is still
// trusted: its deltas apply and its snapshots load (engine/PLAN.md, row 2).
// Only browsers' stamps should be bounded.
func TestFastServerClockIsTrusted(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	ahead := time.Now().Add(5 * time.Minute)
	fast, err := engine.Mock{}.New(ctx, engine.Settings{ID: "p", Replica: "fast-server", Now: func() time.Time { return ahead }})
	if err != nil {
		t.Fatal(err)
	}
	in, _ := engine.MockDelta("", 0, engine.MockSet{Key: "title", Value: "from the fast server"})
	out, err := fast.Edit(ctx, in)
	if err != nil {
		t.Fatal(err)
	}

	// Its delta, read from the stream by this replica's consumer.
	d := wal.Delta{ProjectID: "p", Origin: "fast-server", Seq: 1,
		Delta: engine.DeltaInput{Format: out.Format, Version: out.Version, Payload: out.Payload}}
	if err := w.m.ApplyDelta(ctx, d); err != nil {
		t.Errorf("a fast server's delta was refused: %v", err)
	}

	// Its snapshot, stored as the latest of another project.
	snap, err := fast.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.store.Put(ctx, "q", snap, 0, snapshots.Edit{}); err != nil {
		t.Fatal(err)
	}
	doc, err := w.m.GetDocument(ctx, "ann", "q")
	if err != nil {
		t.Fatalf("a fast server's snapshot didn't load: %v", err)
	}
	if got := registers(t, doc)["title"]; got != "from the fast server" {
		t.Errorf("title = %v", got)
	}
}
