package engine

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"maps"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/mjcourte/modulith/internal/engine/fbs"
)

// Mock is an Engine: an in-memory stand-in for the native engine. It
// is a real, if tiny, CRDT: the document is a map of last-writer-wins
// registers stamped (lamport, replica), and deltas and snapshots use the
// flatbuffers wire format (mock_wire.go). Any register whose value is an
// AssetRef is a use of that asset.
type Mock struct{}

var _ Engine = Mock{}

// New returns an empty mock document.
func (Mock) New(_ context.Context, settings Settings) (Document, error) {
	if settings.ID == "" {
		return nil, fmt.Errorf("engine: settings: empty ID")
	}
	replica := settings.Replica
	if replica == "" {
		replica = rand.Text()
	}
	now, skew := settings.Now, settings.MaxSkew
	if now == nil {
		now = time.Now
	}
	if skew == 0 {
		skew = DefaultMaxSkew
	}
	return &mockDocument{
		id:      settings.ID,
		replica: replica,
		now:     now,
		skew:    skew,
		regs:    map[string]*fbs.RegisterT{},
		loaded:  map[AssetRef]bool{},
	}, nil
}

// Load returns a mock document holding snap. The mock has nothing faster
// than merging into an empty document; the native engine can load directly.
func (m Mock) Load(ctx context.Context, settings Settings, snap Snapshot) (Document, error) {
	d, err := m.New(ctx, settings)
	if err != nil {
		return nil, err
	}
	if err := d.Merge(ctx, snap); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

type mockDocument struct {
	id, replica string
	now         func() time.Time
	skew        time.Duration

	mu     sync.Mutex
	closed bool
	clock  uint64                    // highest lamport seen (Lamport's rule)
	regs   map[string]*fbs.RegisterT // by key; tombstones included
	loaded map[AssetRef]bool         // assets held in memory
}

// lock locks d, or returns ErrClosed and leaves it unlocked.
func (d *mockDocument) lock() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return ErrClosed
	}
	return nil
}

func (d *mockDocument) ID() string { return d.id }

// newer reports whether stamp a is after b: higher lamport, then higher
// replica ID.
func newer(a, b *fbs.StampT) bool {
	if b == nil {
		return true
	}
	if a.Lamport != b.Lamport {
		return a.Lamport > b.Lamport
	}
	return a.Replica > b.Replica
}

// apply writes r stamped st if it wins over the current register. It's
// what makes applying idempotent and order-independent.
func (d *mockDocument) apply(r *fbs.RegisterT, st *fbs.StampT) {
	if cur := d.regs[r.Key]; cur != nil && !wins(r, st, cur) {
		return
	}
	reg := &fbs.RegisterT{Key: r.Key, Removed: r.Removed, Stamp: st}
	if !r.Removed {
		reg.Value = r.Value
	}
	d.regs[r.Key] = reg
	d.clock = max(d.clock, st.Lamport)
}

// wins reports whether r stamped st replaces cur: the later stamp, or, for
// equal stamps, the greater value. Equal stamps normally mean the same write,
// but two writers sharing a replica ID can stamp different values alike;
// comparing the values makes every replica pick the same one
// (engine/PLAN.md row 1).
func wins(r *fbs.RegisterT, st *fbs.StampT, cur *fbs.RegisterT) bool {
	if *st != *cur.Stamp {
		return newer(st, cur.Stamp)
	}
	return valueKey(r) > valueKey(cur)
}

// valueKey orders register contents for tie-breaks: the value's type, then
// its JSON form. A delete is "", below every value.
func valueKey(r *fbs.RegisterT) string {
	if r.Removed {
		return ""
	}
	b, _ := json.Marshal(fromValue(r.Value)) // plain values always marshal
	return fmt.Sprintf("%d:%s", r.Value.Type, b)
}

func checkRegister(r *fbs.RegisterT) error {
	if r.Key == "" {
		return fmt.Errorf("engine: register without a key")
	}
	if !r.Removed && (r.Value == nil || r.Value.Type == fbs.ValueNONE) {
		return fmt.Errorf("engine: register %q has no value", r.Key)
	}
	return nil
}

// Edit applies a delta. An unstamped one (lamport 0) is a local edit: it
// gets this replica's next clock tick, and the returned delta carries it.
func (d *mockDocument) Edit(ctx context.Context, in DeltaInput) (Delta, error) {
	delta, err := decodeDelta(in)
	if err != nil {
		return Delta{}, err
	}
	for _, r := range delta.Sets {
		if err := checkRegister(r); err != nil {
			return Delta{}, err
		}
	}
	if delta.Lamport != 0 && delta.Replica == "" {
		return Delta{}, fmt.Errorf("engine: stamped delta without a replica")
	}
	if err := d.lock(); err != nil {
		return Delta{}, err
	}
	defer d.mu.Unlock()
	if delta.Lamport == 0 {
		delta.Lamport, delta.Replica = d.tick(), d.replica
	} else if !in.Trusted {
		if err := d.checkStamp(delta.Lamport); err != nil {
			return Delta{}, err
		}
	}
	st := &fbs.StampT{Lamport: delta.Lamport, Replica: delta.Replica}
	for _, r := range delta.Sets {
		d.apply(r, st)
	}
	return Delta{Format: WireNative, Version: mockVersion, Payload: encodeDelta(delta)}, nil
}

// Merge doesn't bound stamps (MaxSkew): snapshots come only from trusted
// servers (storage, another replica), whose clocks may legitimately run
// ahead of this one's (engine/PLAN.md row 2).
func (d *mockDocument) Merge(ctx context.Context, s Snapshot) error {
	snap, err := decodeSnapshot(s)
	if err != nil {
		return err
	}
	for _, r := range snap.Registers {
		if err := checkRegister(r); err != nil {
			return err
		}
		if r.Stamp == nil || r.Stamp.Lamport == 0 || r.Stamp.Replica == "" {
			return fmt.Errorf("engine: snapshot register %q is unstamped", r.Key)
		}
	}
	if err := d.lock(); err != nil {
		return err
	}
	defer d.mu.Unlock()
	for _, r := range snap.Registers {
		d.apply(r, r.Stamp)
	}
	d.clock = max(d.clock, snap.Clock)
	return nil
}

// Snapshot writes the registers sorted by key, so replicas holding the same
// state produce the same bytes.
func (d *mockDocument) Snapshot(ctx context.Context) (Snapshot, error) {
	if err := d.lock(); err != nil {
		return Snapshot{}, err
	}
	defer d.mu.Unlock()
	snap := &fbs.SnapshotT{Clock: d.clock, Registers: []*fbs.RegisterT{}}
	for _, k := range slices.Sorted(maps.Keys(d.regs)) {
		snap.Registers = append(snap.Registers, d.regs[k])
	}
	return Snapshot{Format: WireNative, Version: mockVersion, Payload: encodeSnapshot(snap)}, nil
}

// Query ignores the query and returns every live register:
// {"clock": n, "registers": {key: value}}.
func (d *mockDocument) Query(ctx context.Context, q Query) (*View, error) {
	if err := checkQuery(q); err != nil {
		return nil, err
	}
	if err := d.lock(); err != nil {
		return nil, err
	}
	defer d.mu.Unlock()
	regs := map[string]any{}
	for k, r := range d.regs {
		if !r.Removed {
			regs[k] = fromValue(r.Value)
		}
	}
	data, err := json.Marshal(map[string]any{"clock": d.clock, "registers": regs})
	if err != nil {
		return nil, err
	}
	return &View{Data: data}, nil
}

// RenderImage returns a blank PNG of the requested size.
func (d *mockDocument) RenderImage(ctx context.Context, q Query, opts ImageOptions) (RenderedImage, error) {
	if err := checkQuery(q); err != nil {
		return RenderedImage{}, err
	}
	if opts.Format != "png" {
		return RenderedImage{}, fmt.Errorf("%w: image %q", ErrUnsupportedFormat, opts.Format)
	}
	if opts.Width <= 0 || opts.Height <= 0 {
		return RenderedImage{}, fmt.Errorf("engine: render: invalid size %dx%d", opts.Width, opts.Height)
	}
	if err := d.lock(); err != nil {
		return RenderedImage{}, err
	}
	d.mu.Unlock() // only the closed check needs the lock
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, opts.Width, opts.Height))); err != nil {
		return RenderedImage{}, err
	}
	return RenderedImage{Data: buf.Bytes(), ContentType: "image/png", Width: opts.Width, Height: opts.Height}, nil
}

func (d *mockDocument) RenderVideo(ctx context.Context, q Query, opts VideoOptions) (RenderedVideo, error) {
	if err := d.lock(); err != nil {
		return RenderedVideo{}, err
	}
	d.mu.Unlock()
	return RenderedVideo{}, fmt.Errorf("%w: the mock engine renders no video", ErrUnsupportedFormat)
}

func (d *mockDocument) LoadAssets(ctx context.Context, f AssetFilter) error {
	return d.setLoaded(f, true)
}

func (d *mockDocument) UnloadAssets(ctx context.Context, f AssetFilter) error {
	return d.setLoaded(f, false)
}

func (d *mockDocument) setLoaded(f AssetFilter, loaded bool) error {
	if err := d.lock(); err != nil {
		return err
	}
	defer d.mu.Unlock()
	refs, err := d.assets(f)
	if err != nil {
		return err
	}
	for _, a := range refs {
		d.loaded[a] = loaded
	}
	return nil
}

func (d *mockDocument) Assets(ctx context.Context, f AssetFilter) ([]AssetRef, error) {
	if err := d.lock(); err != nil {
		return nil, err
	}
	defer d.mu.Unlock()
	return d.assets(f)
}

// assets returns the distinct asset versions live registers use, sorted,
// or ErrNotFound if f names an asset the document doesn't use.
func (d *mockDocument) assets(f AssetFilter) ([]AssetRef, error) {
	used := map[AssetRef]bool{}
	for _, r := range d.regs {
		if a, ok := assetOf(r.Value); ok && !r.Removed {
			used[a] = true
		}
	}
	usedIDs := map[string]bool{}
	for a := range used {
		usedIDs[a.ID] = true
	}
	for _, id := range f.IDs {
		if !usedIDs[id] {
			return nil, fmt.Errorf("%w: asset %q", ErrNotFound, id)
		}
	}
	var out []AssetRef
	for a := range used {
		if len(f.IDs) == 0 || slices.Contains(f.IDs, a.ID) {
			out = append(out, a)
		}
	}
	slices.SortFunc(out, func(a, b AssetRef) int {
		return cmp.Or(cmp.Compare(a.ID, b.ID), cmp.Compare(a.Version, b.Version))
	})
	return out, nil
}

// ReplaceAssetRefs rewrites, as one local edit, every live register using
// a key of refs. If nothing uses them, it changes nothing and returns a
// Delta with an empty Payload.
func (d *mockDocument) ReplaceAssetRefs(ctx context.Context, refs map[AssetRef]AssetRef) (Delta, error) {
	if err := d.lock(); err != nil {
		return Delta{}, err
	}
	defer d.mu.Unlock()
	var sets []*fbs.RegisterT
	for _, k := range slices.Sorted(maps.Keys(d.regs)) {
		r := d.regs[k]
		if a, ok := assetOf(r.Value); ok && !r.Removed {
			if to, ok := refs[a]; ok {
				v, _ := toValue(to) // an AssetRef always converts
				sets = append(sets, &fbs.RegisterT{Key: k, Value: v})
			}
		}
	}
	if len(sets) == 0 {
		return Delta{Format: WireNative, Version: mockVersion}, nil
	}
	delta := &fbs.DeltaT{Replica: d.replica, Lamport: d.tick(), Sets: sets}
	st := &fbs.StampT{Lamport: delta.Lamport, Replica: delta.Replica}
	for _, r := range sets {
		d.apply(r, st)
	}
	return Delta{Format: WireNative, Version: mockVersion, Payload: encodeDelta(delta)}, nil
}

func (d *mockDocument) Close() error {
	if err := d.lock(); err != nil {
		return err
	}
	defer d.mu.Unlock()
	d.closed = true
	return nil
}

func checkQuery(q Query) error {
	if q.Format != QueryNative && q.Format != QueryGraphQL {
		return fmt.Errorf("%w: query %q", ErrUnsupportedFormat, q.Format)
	}
	return nil
}

// tick advances the clock for a local edit (HLC: never behind wall time)
// and returns the new stamp. d.mu must be held.
func (d *mockDocument) tick() uint64 {
	d.clock = max(d.clock+1, HLC(d.now()))
	return d.clock
}

// checkStamp rejects a received stamp too far ahead of this replica's wall
// clock to be honest. d.mu must be held.
func (d *mockDocument) checkStamp(stamp uint64) error {
	if limit := MaxStamp(d.now(), d.skew); stamp > limit {
		return fmt.Errorf("%w: %d is past %d (now + %v)", ErrBadStamp, stamp, limit, d.skew)
	}
	return nil
}

// RevertTo diffs the live registers against snap's and writes the
// difference as one fresh edit. snap's stamps are ignored: its values come
// back newer than anything now in the document.
func (d *mockDocument) RevertTo(ctx context.Context, s Snapshot) (Delta, error) {
	snap, err := decodeSnapshot(s)
	if err != nil {
		return Delta{}, err
	}
	target := map[string]*fbs.RegisterT{} // live keys only
	for _, r := range snap.Registers {
		if err := checkRegister(r); err != nil {
			return Delta{}, err
		}
		if !r.Removed {
			target[r.Key] = r
		}
	}
	if err := d.lock(); err != nil {
		return Delta{}, err
	}
	defer d.mu.Unlock()
	keys := slices.Sorted(maps.Keys(d.regs))
	for k := range target {
		if _, ok := d.regs[k]; !ok {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	var sets []*fbs.RegisterT
	for _, k := range keys {
		cur, want := d.regs[k], target[k]
		live := cur != nil && !cur.Removed
		switch {
		case want != nil && (!live || !reflect.DeepEqual(fromValue(cur.Value), fromValue(want.Value))):
			sets = append(sets, &fbs.RegisterT{Key: k, Value: want.Value})
		case want == nil && live:
			sets = append(sets, &fbs.RegisterT{Key: k, Removed: true})
		}
	}
	if len(sets) == 0 {
		return Delta{Format: WireNative, Version: mockVersion}, nil
	}
	delta := &fbs.DeltaT{Replica: d.replica, Lamport: d.tick(), Sets: sets}
	st := &fbs.StampT{Lamport: delta.Lamport, Replica: delta.Replica}
	for _, r := range sets {
		d.apply(r, st)
	}
	return Delta{Format: WireNative, Version: mockVersion, Payload: encodeDelta(delta)}, nil
}
