package engine_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/mjcourte/modulith/internal/engine"
)

var _ engine.Engine = engine.Mock{}

func openMock(t *testing.T, replica string) engine.Document {
	t.Helper()
	d, err := engine.Mock{}.New(context.Background(), engine.Settings{ID: "doc", Replica: replica})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func mockDelta(t *testing.T, replica string, lamport uint64, sets ...engine.MockSet) engine.DeltaInput {
	t.Helper()
	in, err := engine.MockDelta(replica, lamport, sets...)
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func edit(t *testing.T, d engine.Document, in engine.DeltaInput) engine.Delta {
	t.Helper()
	out, err := d.Edit(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func snapshot(t *testing.T, d engine.Document) engine.Snapshot {
	t.Helper()
	s, err := d.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// registers returns the live registers from the JSON view.
func registers(t *testing.T, d engine.Document) map[string]any {
	t.Helper()
	v, err := d.Query(context.Background(), engine.Query{Format: engine.QueryNative})
	if err != nil {
		t.Fatal(err)
	}
	var view struct{ Registers map[string]any }
	if err := json.Unmarshal(v.Data, &view); err != nil {
		t.Fatalf("view is not JSON: %v", err)
	}
	return view.Registers
}

// asDelta turns an Edit result back into input, as a receiving replica gets it.
func asDelta(d engine.Delta) engine.DeltaInput {
	return engine.DeltaInput{Format: d.Format, Version: d.Version, Payload: d.Payload}
}

// Replicas that apply the same deltas in any order, any number of times,
// converge to byte-identical snapshots.
func TestMockConverges(t *testing.T) {
	a, b := openMock(t, "a"), openMock(t, "b")
	var deltas []engine.DeltaInput
	// Concurrent writes to "title" at the same lamport: the tie goes to replica b.
	deltas = append(deltas, asDelta(edit(t, a, mockDelta(t, "", 0, engine.MockSet{Key: "title", Value: "from a"}))))
	deltas = append(deltas, asDelta(edit(t, b, mockDelta(t, "", 0, engine.MockSet{Key: "title", Value: "from b"}))))
	deltas = append(deltas, asDelta(edit(t, a, mockDelta(t, "", 0,
		engine.MockSet{Key: "w", Value: 1920},
		engine.MockSet{Key: "img", Value: engine.AssetRef{ID: "x", Version: 2}}))))
	deltas = append(deltas, asDelta(edit(t, a, mockDelta(t, "", 0, engine.MockSet{Key: "w", Removed: true}))))

	// Each replica gets every delta (its own echoes too), shuffled and repeated.
	for _, d := range []engine.Document{a, b} {
		shuffled := slices.Concat(deltas, deltas)
		rand.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		for _, in := range shuffled {
			edit(t, d, in)
		}
	}
	sa, sb := snapshot(t, a), snapshot(t, b)
	if !bytes.Equal(sa.Payload, sb.Payload) {
		t.Fatalf("replicas diverged:\na %v\nb %v", registers(t, a), registers(t, b))
	}
	want := map[string]any{"title": "from b", "img": map[string]any{"assetId": "x", "version": float64(2)}}
	if got := registers(t, a); !equalJSON(got, want) {
		t.Errorf("registers = %v, want %v", got, want)
	}
}

// A snapshot merged into a fresh replica reproduces the state, and replaying
// deltas the snapshot already covers changes nothing.
func TestMockSnapshotMergeAndReplay(t *testing.T) {
	a := openMock(t, "a")
	d1 := asDelta(edit(t, a, mockDelta(t, "", 0, engine.MockSet{Key: "k", Value: "v1"})))
	d2 := asDelta(edit(t, a, mockDelta(t, "", 0, engine.MockSet{Key: "k", Value: "v2"})))
	snap := snapshot(t, a)

	b := openMock(t, "b")
	if err := b.Merge(context.Background(), snap); err != nil {
		t.Fatal(err)
	}
	edit(t, b, d2)
	edit(t, b, d1) // older than what the snapshot holds: must lose
	if got := snapshot(t, b); !bytes.Equal(got.Payload, snap.Payload) {
		t.Errorf("after replay: %v, want the snapshot's state %v", registers(t, b), registers(t, a))
	}

	// b's next local edit is stamped after everything it has seen.
	out := edit(t, b, mockDelta(t, "", 0, engine.MockSet{Key: "k", Value: "v3"}))
	edit(t, a, asDelta(out))
	if got := registers(t, a)["k"]; got != "v3" {
		t.Errorf("a's k = %v after b's later edit, want v3 (b's clock didn't advance)", got)
	}
}

func TestMockRejectsBadInput(t *testing.T) {
	ctx := context.Background()
	d := openMock(t, "a")
	good := mockDelta(t, "", 0, engine.MockSet{Key: "k", Value: "v"})
	snap := snapshot(t, d)

	cases := map[string]engine.DeltaInput{
		"json format":               {Format: engine.WireJSON, Version: good.Version, Payload: []byte(`{}`)},
		"wrong version":             {Format: good.Format, Version: 99, Payload: good.Payload},
		"snapshot bytes as a delta": {Format: good.Format, Version: good.Version, Payload: snap.Payload},
	}
	for name, in := range cases {
		if _, err := d.Edit(ctx, in); !errors.Is(err, engine.ErrUnsupportedFormat) {
			t.Errorf("%s: got %v, want ErrUnsupportedFormat", name, err)
		}
	}
	corrupt := slices.Clone(good.Payload)
	corrupt = corrupt[:len(corrupt)/2]
	if _, err := d.Edit(ctx, engine.DeltaInput{Format: good.Format, Version: good.Version, Payload: corrupt}); err == nil {
		t.Error("truncated delta: got nil error")
	}
	if _, err := engine.MockDelta("", 0, engine.MockSet{Key: "k", Value: struct{}{}}); err == nil {
		t.Error("unsupported value type: got nil error")
	}
	if err := d.Merge(ctx, engine.Snapshot{Format: good.Format, Version: good.Version, Payload: good.Payload}); !errors.Is(err, engine.ErrUnsupportedFormat) {
		t.Errorf("delta bytes as a snapshot: got %v, want ErrUnsupportedFormat", err)
	}
}

func TestMockAssets(t *testing.T) {
	ctx := context.Background()
	x1, x2, y1 := engine.AssetRef{ID: "x", Version: 1}, engine.AssetRef{ID: "x", Version: 2}, engine.AssetRef{ID: "y", Version: 1}
	d := openMock(t, "a")
	edit(t, d, mockDelta(t, "", 0,
		engine.MockSet{Key: "bg", Value: x1},
		engine.MockSet{Key: "logo", Value: x1}, // a second use of the same version
		engine.MockSet{Key: "photo", Value: y1}))
	edit(t, d, mockDelta(t, "", 0, engine.MockSet{Key: "photo", Removed: true}))

	got, err := d.Assets(ctx, engine.AssetFilter{})
	if err != nil || !slices.Equal(got, []engine.AssetRef{x1}) {
		t.Fatalf("Assets = %v, %v; want [x@1] (y removed, x deduplicated)", got, err)
	}
	if err := d.LoadAssets(ctx, engine.AssetFilter{IDs: []string{"y"}}); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("load unused asset: got %v, want ErrNotFound", err)
	}

	// Replacing rewrites every use, as one edit other replicas can apply.
	other := openMock(t, "b")
	out, err := d.ReplaceAssetRefs(ctx, map[engine.AssetRef]engine.AssetRef{x1: x2, y1: x2})
	if err != nil {
		t.Fatal(err)
	}
	edit(t, other, asDelta(out))
	for _, doc := range []engine.Document{d, other} {
		got, _ := doc.Assets(ctx, engine.AssetFilter{})
		if !slices.Equal(got, []engine.AssetRef{x2}) {
			t.Errorf("after replace: Assets = %v, want [x@2]", got)
		}
	}
	if regs := registers(t, other); len(regs) != 2 {
		t.Errorf("replica b registers = %v, want bg and logo only", regs)
	}

	none, err := d.ReplaceAssetRefs(ctx, map[engine.AssetRef]engine.AssetRef{y1: x1})
	if err != nil || len(none.Payload) != 0 {
		t.Errorf("replacing an unused ref: payload %d bytes, %v; want empty, nil", len(none.Payload), err)
	}
}

func TestMockRenderImage(t *testing.T) {
	ctx := context.Background()
	d := openMock(t, "a")
	img, err := d.RenderImage(ctx, engine.Query{Format: engine.QueryNative}, engine.ImageOptions{Format: "png", Width: 30, Height: 20})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(img.Data))
	if err != nil || cfg.Width != 30 || cfg.Height != 20 {
		t.Fatalf("decoded %dx%d, %v; want 30x20", cfg.Width, cfg.Height, err)
	}
}

func TestMockClosed(t *testing.T) {
	ctx := context.Background()
	d := openMock(t, "a")
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Snapshot(ctx); !errors.Is(err, engine.ErrClosed) {
		t.Fatalf("Snapshot after Close: got %v, want ErrClosed", err)
	}
	if err := d.Close(); !errors.Is(err, engine.ErrClosed) {
		t.Fatalf("second Close: got %v, want ErrClosed", err)
	}
}

func equalJSON(a, b any) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return bytes.Equal(ja, jb)
}

// fakeNow is a wall clock tests move by hand.
type fakeNow struct{ t time.Time }

func (f *fakeNow) now() time.Time { return f.t }

func openAt(t *testing.T, replica string, clock *fakeNow) engine.Document {
	t.Helper()
	d, err := engine.Mock{}.New(context.Background(), engine.Settings{ID: "doc", Replica: replica, Now: clock.now})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// A peer that was offline while a live peer made many edits must not come
// back chronically behind: its next write, made later in real time, wins.
// With a plain Lamport counter it would be stamped 1 and lose to all of
// them until it heard from the live peer.
func TestMockIntermittentPeerIsNotLeftBehind(t *testing.T) {
	clock := &fakeNow{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	peerClock := &fakeNow{t: clock.t} // the intermittent peer's own wall clock
	live, intermittent, observer := openAt(t, "live", clock), openAt(t, "intermittent", peerClock), openAt(t, "observer", clock)

	var liveDeltas []engine.DeltaInput
	for i := range 500 { // a busy peer: many edits within the same few ms
		out := edit(t, live, mockDelta(t, "", 0, engine.MockSet{Key: "title", Value: fmt.Sprint("live ", i)}))
		liveDeltas = append(liveDeltas, asDelta(out))
	}
	clock.t = clock.t.Add(5 * time.Second) // the intermittent peer, offline all along, edits later
	peerClock.t = clock.t
	late := asDelta(edit(t, intermittent, mockDelta(t, "", 0, engine.MockSet{Key: "title", Value: "intermittent, later"})))

	for _, in := range append(liveDeltas, late) {
		edit(t, observer, in)
	}
	if got := registers(t, observer)["title"]; got != "intermittent, later" {
		t.Errorf("title = %v: the intermittent peer's later write lost", got)
	}

	// And a peer that has heard a stamp keeps stamping after it, even if
	// its own wall clock is behind (Lamport's rule still holds). Behind by
	// 30s, inside MaxSkew: a stamp it hears from the live peer's future is
	// still accepted.
	clock.t = clock.t.Add(30 * time.Second)
	peerClock.t = clock.t.Add(-30 * time.Second)
	newer := asDelta(edit(t, live, mockDelta(t, "", 0, engine.MockSet{Key: "title", Value: "live, newest"})))
	edit(t, intermittent, newer)
	edit(t, observer, newer)
	after := asDelta(edit(t, intermittent, mockDelta(t, "", 0, engine.MockSet{Key: "title", Value: "after hearing"})))
	edit(t, observer, after)
	if got := registers(t, observer)["title"]; got != "after hearing" {
		t.Errorf("title = %v: a write after a received one lost to it", got)
	}
}

// Stamps can't be forged into the future: an untrusted delta stamped past
// now + MaxSkew is refused, so it can't win every write or drag clocks.
// Trusted deltas and snapshots come from servers, whose clocks may run
// ahead: they aren't bounded (engine/PLAN.md row 2).
func TestMockRejectsFutureStamps(t *testing.T) {
	clock := &fakeNow{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	d := openAt(t, "server", clock)
	ctx := context.Background()

	ok := engine.HLC(clock.t.Add(30 * time.Second)) // within the default 1 minute skew
	if _, err := d.Edit(ctx, mockDelta(t, "browser", ok, engine.MockSet{Key: "k", Value: "skewed but fine"})); err != nil {
		t.Fatalf("a stamp within the skew was refused: %v", err)
	}
	for name, stamp := range map[string]uint64{
		"an hour ahead": engine.HLC(clock.t.Add(time.Hour)),
		"max uint64":    ^uint64(0),
	} {
		if _, err := d.Edit(ctx, mockDelta(t, "attacker", stamp, engine.MockSet{Key: "k", Value: "forged"})); !errors.Is(err, engine.ErrBadStamp) {
			t.Errorf("delta stamped %s: got %v, want ErrBadStamp", name, err)
		}
	}
	if got := registers(t, d)["k"]; got != "skewed but fine" {
		t.Errorf("k = %v after forged deltas, want them refused", got)
	}

	trusted := mockDelta(t, "other-server", engine.HLC(clock.t.Add(time.Hour)), engine.MockSet{Key: "k", Value: "from a fast server"})
	trusted.Trusted = true
	if _, err := d.Edit(ctx, trusted); err != nil {
		t.Errorf("a trusted delta an hour ahead: %v", err)
	}

	// A snapshot from a server whose clock ran far ahead merges.
	future := &fakeNow{t: clock.t.Add(24 * time.Hour)}
	ahead := openAt(t, "ahead", future)
	edit(t, ahead, mockDelta(t, "", 0, engine.MockSet{Key: "k", Value: "from tomorrow"}))
	if err := d.Merge(ctx, snapshot(t, ahead)); err != nil {
		t.Errorf("merging a snapshot stamped tomorrow: %v", err)
	}
	if got := registers(t, d)["k"]; got != "from tomorrow" {
		t.Errorf("k = %v after the merge", got)
	}
}

// Load opens a document holding exactly a snapshot's state, with its clock:
// its next local edit is stamped after everything the snapshot holds.
func TestMockLoad(t *testing.T) {
	ctx := context.Background()
	a := openMock(t, "a")
	for i := range 3 {
		edit(t, a, mockDelta(t, "", 0, engine.MockSet{Key: "k", Value: fmt.Sprint(i)}))
	}
	snap := snapshot(t, a)

	b, err := engine.Mock{}.Load(ctx, engine.Settings{ID: "doc", Replica: "b"}, snap)
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot(t, b); !bytes.Equal(got.Payload, snap.Payload) {
		t.Errorf("loaded state %v, want the snapshot's %v", registers(t, b), registers(t, a))
	}
	edit(t, a, asDelta(edit(t, b, mockDelta(t, "", 0, engine.MockSet{Key: "k", Value: "after load"}))))
	if got := registers(t, a)["k"]; got != "after load" {
		t.Errorf("k = %v: an edit after Load lost to the snapshot's", got)
	}

	if _, err := (engine.Mock{}).Load(ctx, engine.Settings{ID: "doc"}, engine.Snapshot{Format: engine.WireNative, Version: 1, Payload: []byte("garbage!")}); err == nil {
		t.Error("loading garbage succeeded")
	}
}

// RevertTo brings back a snapshot's state as one fresh edit: keys changed
// since are set back, keys added since are deleted, keys deleted since come
// back. Other replicas applying the edit get the same state, and an older
// concurrent edit can't undo it.
func TestMockRevertTo(t *testing.T) {
	ctx := context.Background()
	x1, x2 := engine.AssetRef{ID: "x", Version: 1}, engine.AssetRef{ID: "x", Version: 2}
	a, b := openMock(t, "a"), openMock(t, "b")
	both := func(in engine.DeltaInput) engine.DeltaInput {
		t.Helper()
		out := asDelta(edit(t, a, in))
		edit(t, b, out)
		return out
	}
	both(mockDelta(t, "", 0, engine.MockSet{Key: "title", Value: "v1"}, engine.MockSet{Key: "w", Value: 100}, engine.MockSet{Key: "img", Value: x1}))
	good := snapshot(t, a)
	want := registers(t, a)

	stale := asDelta(edit(t, b, mockDelta(t, "", 0, engine.MockSet{Key: "title", Value: "older, concurrent"})))
	both(mockDelta(t, "", 0, engine.MockSet{Key: "title", Value: "v2"}, engine.MockSet{Key: "w", Removed: true},
		engine.MockSet{Key: "extra", Value: "added later"}, engine.MockSet{Key: "img", Value: x2}))

	revert, err := a.RevertTo(ctx, good)
	if err != nil || len(revert.Payload) == 0 {
		t.Fatalf("RevertTo: %d bytes, %v", len(revert.Payload), err)
	}
	if got := registers(t, a); !equalJSON(got, want) {
		t.Errorf("after revert: %v, want %v", got, want)
	}
	if assets, _ := a.Assets(ctx, engine.AssetFilter{}); !slices.Equal(assets, []engine.AssetRef{x1}) {
		t.Errorf("assets after revert = %v, want [x@1]", assets)
	}
	edit(t, b, asDelta(revert))
	edit(t, a, stale) // made before the revert: must lose
	edit(t, b, stale)
	for name, d := range map[string]engine.Document{"a": a, "b": b} {
		if got := registers(t, d); !equalJSON(got, want) {
			t.Errorf("%s: %v, want the reverted state %v", name, got, want)
		}
	}

	again, err := a.RevertTo(ctx, good)
	if err != nil || len(again.Payload) != 0 {
		t.Errorf("reverting to the current state: %d bytes, %v; want an empty delta", len(again.Payload), err)
	}
}

// Two writers sharing a replica ID (two tabs, a reused ID) can stamp
// different values with the same stamp. Each replica must still converge on
// one of them (engine/PLAN.md, row 1).
func TestMockConvergesWhenReplicaIDsCollide(t *testing.T) {

	clock := &fakeNow{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	a, b := openAt(t, "tab", clock), openAt(t, "tab", clock)
	fromA := asDelta(edit(t, a, mockDelta(t, "", 0, engine.MockSet{Key: "title", Value: "from a"})))
	fromB := asDelta(edit(t, b, mockDelta(t, "", 0, engine.MockSet{Key: "title", Value: "from b"})))
	edit(t, a, fromB)
	edit(t, b, fromA)
	if ra, rb := registers(t, a)["title"], registers(t, b)["title"]; ra != rb {
		t.Errorf("replicas diverged: a has %v, b has %v", ra, rb)
	}
	if !bytes.Equal(snapshot(t, a).Payload, snapshot(t, b).Payload) {
		t.Error("snapshots differ")
	}
}

// StampOf reads what Edit would see: the stamp a client put on its delta, or
// none, and the stamp a document put on a local edit.
func TestStampOf(t *testing.T) {
	lamport, replica, err := engine.StampOf(mockDelta(t, "tab-1", 42, engine.MockSet{Key: "k", Value: "v"}))
	if err != nil || lamport != 42 || replica != "tab-1" {
		t.Errorf("stamped: got %d %q %v", lamport, replica, err)
	}
	unstamped := mockDelta(t, "", 0, engine.MockSet{Key: "k", Value: "v"})
	if lamport, replica, err := engine.StampOf(unstamped); err != nil || lamport != 0 || replica != "" {
		t.Errorf("unstamped: got %d %q %v", lamport, replica, err)
	}
	out := edit(t, openMock(t, "server"), unstamped)
	if lamport, replica, err := engine.StampOf(asDelta(out)); err != nil || lamport == 0 || replica != "server" {
		t.Errorf("local edit: got %d %q %v", lamport, replica, err)
	}
	bad := engine.DeltaInput{Format: engine.WireNative, Version: 1, Payload: []byte("not a delta")}
	if _, _, err := engine.StampOf(bad); !errors.Is(err, engine.ErrUnsupportedFormat) {
		t.Errorf("garbage: got %v, want ErrUnsupportedFormat", err)
	}
}
