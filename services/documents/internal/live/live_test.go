package live_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gyoza/internal/engine"
	"gyoza/services/documents/internal/live"
)

var testConfig = live.Config{Debounce: 2 * time.Second, MaxDelay: 30 * time.Second, TTL: 10 * time.Minute}

// harness is a registry over the mock engine with an in-memory snapshot
// store, counting loads and saves.
type harness struct {
	t     *testing.T
	reg   *live.Registry
	clock *live.FakeClock

	mu       sync.Mutex
	store    map[string]engine.Snapshot
	loads    int
	saves    int
	failNext int           // saves to fail before succeeding again
	onSave   func()        // runs inside the save, before it stores
	loadGate chan struct{} // if set, loads wait for it
	lastEdit live.Edit     // what the latest save was told
}

func newHarness(t *testing.T) *harness {
	h := &harness{t: t, store: map[string]engine.Snapshot{}}
	h.reg, h.clock = live.NewWithFakeClock(h.load, h.save, testConfig)
	return h
}

func (h *harness) load(ctx context.Context, id string) (engine.Document, uint64, error) {
	h.mu.Lock()
	h.loads++
	gate := h.loadGate
	snap, ok := h.store[id]
	h.mu.Unlock()
	if gate != nil {
		<-gate
	}
	doc, err := engine.Mock{}.New(ctx, engine.Settings{ID: id, Replica: "server"})
	if err != nil {
		return nil, 0, err
	}
	if ok {
		if err := doc.Merge(ctx, snap); err != nil {
			return nil, 0, err
		}
	}
	return doc, 0, nil
}

func (h *harness) save(ctx context.Context, id string, _ engine.Document, snap engine.Snapshot, seq uint64, last live.Edit) (uint64, error) {
	h.mu.Lock()
	hook := h.onSave
	h.onSave = nil
	h.mu.Unlock()
	if hook != nil {
		hook()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.failNext > 0 {
		h.failNext--
		return 0, errors.New("storage unavailable")
	}
	h.saves++
	h.store[id] = snap
	h.lastEdit = last
	return seq, nil
}

func (h *harness) counts() (loads, saves int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.loads, h.saves
}

func (h *harness) acquire(id string) (*live.Doc, func()) {
	h.t.Helper()
	d, release, err := h.reg.Acquire(context.Background(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	return d, release
}

// set writes key=value as a local edit.
func (h *harness) set(d *live.Doc, key, value string) {
	h.t.Helper()
	in, err := engine.MockDelta("", 0, engine.MockSet{Key: key, Value: value})
	if err != nil {
		h.t.Fatal(err)
	}
	err = d.Update(0, "ann", func(doc engine.Document) error {
		_, err := doc.Edit(context.Background(), in)
		return err
	})
	if err != nil {
		h.t.Fatal(err)
	}
}

// stored returns key's value in the project's stored snapshot.
func (h *harness) stored(id, key string) any {
	h.t.Helper()
	h.mu.Lock()
	snap, ok := h.store[id]
	h.mu.Unlock()
	if !ok {
		return nil
	}
	doc, _ := engine.Mock{}.New(context.Background(), engine.Settings{ID: id})
	if err := doc.Merge(context.Background(), snap); err != nil {
		h.t.Fatal(err)
	}
	v, err := doc.Query(context.Background(), engine.Query{Format: engine.QueryNative})
	if err != nil {
		h.t.Fatal(err)
	}
	var view struct{ Registers map[string]any }
	if err := json.Unmarshal(v.Data, &view); err != nil {
		h.t.Fatal(err)
	}
	return view.Registers[key]
}

func (h *harness) wantSaves(want int, when string) {
	h.t.Helper()
	if _, saves := h.counts(); saves != want {
		h.t.Fatalf("%s: %d saves, want %d", when, saves, want)
	}
}

func TestLiveDebounceSavesOnceAfterQuiet(t *testing.T) {
	h := newHarness(t)
	d, release := h.acquire("p")
	defer release()

	for _, v := range []string{"1", "2", "3"} { // a burst, 1s apart
		h.set(d, "k", v)
		h.clock.Advance(time.Second)
	}
	h.wantSaves(0, "during the burst")
	h.clock.Advance(time.Second) // 2s after the last change
	h.wantSaves(1, "after the quiet period")
	if got := h.stored("p", "k"); got != "3" {
		t.Errorf("stored k = %v, want 3", got)
	}
	h.clock.Advance(time.Minute)
	h.wantSaves(1, "with no further changes")
}

func TestLiveMaxDelayCapsConstantEditing(t *testing.T) {
	h := newHarness(t)
	d, release := h.acquire("p")
	defer release()

	for i := range 31 { // a change every second never goes quiet for 2s
		h.set(d, "k", string(rune('a'+i%26)))
		h.clock.Advance(time.Second)
	}
	h.wantSaves(1, "after 31s of constant editing")
}

func TestLiveChangeDuringSaveStaysDirty(t *testing.T) {
	h := newHarness(t)
	d, release := h.acquire("p")
	defer release()

	h.set(d, "k", "before")
	h.onSave = func() { h.set(d, "k", "during") } // lands after the snapshot was taken
	h.clock.Advance(2 * time.Second)
	h.wantSaves(1, "first save")
	if got := h.stored("p", "k"); got != "before" {
		t.Fatalf("first save stored %v, want before", got)
	}
	h.clock.Advance(2 * time.Second)
	h.wantSaves(2, "the change made during the save")
	if got := h.stored("p", "k"); got != "during" {
		t.Errorf("second save stored %v, want during", got)
	}
}

func TestLiveFailedSaveRetries(t *testing.T) {
	h := newHarness(t)
	d, release := h.acquire("p")
	defer release()

	h.failNext = 2
	h.set(d, "k", "v")
	h.clock.Advance(2 * time.Second) // fails; retry in 2s
	h.clock.Advance(2 * time.Second) // fails; retry in 4s
	h.wantSaves(0, "after two failures")
	h.clock.Advance(4 * time.Second)
	h.wantSaves(1, "after the backoff")
}

func TestLiveTTLExtendsOnAccess(t *testing.T) {
	h := newHarness(t)
	_, release := h.acquire("p")
	release()
	h.clock.Advance(5 * time.Minute)
	_, release = h.acquire("p") // an access extends the TTL
	release()
	h.clock.Advance(9 * time.Minute)
	_, release = h.acquire("p") // loads again if it was evicted
	release()
	if loads, _ := h.counts(); loads != 1 {
		t.Fatalf("evicted before the TTL since the last access")
	}
	h.clock.Advance(11 * time.Minute)
	h.acquire("p")
	if loads, _ := h.counts(); loads != 2 {
		t.Fatalf("%d loads, want 2: an idle doc wasn't evicted", loads)
	}
}

// Eviction must store unsaved changes itself. With MaxDelay at an hour the
// debounce retries back off to 2, 6, 14, ... 510s, then 1022s, so the only
// save between 550s and 700s is the eviction's (TTL 10m after the release).
func TestLiveEvictionSavesFirst(t *testing.T) {
	h := newHarness(t)
	cfg := testConfig
	cfg.MaxDelay = time.Hour
	h.reg, h.clock = live.NewWithFakeClock(h.load, h.save, cfg)

	d, release := h.acquire("p")
	h.failNext = 1000
	h.set(d, "k", "v")
	release()
	h.clock.Advance(550 * time.Second)
	h.wantSaves(0, "while storage is down")

	h.mu.Lock()
	h.failNext = 0
	h.mu.Unlock()
	h.clock.Advance(150 * time.Second)
	h.wantSaves(1, "after eviction")
	if got := h.stored("p", "k"); got != "v" {
		t.Fatalf("stored k = %v, want v", got)
	}
	if err := d.Update(0, "ann", func(engine.Document) error { return nil }); err == nil {
		t.Error("Update on an evicted doc succeeded")
	}
	h.acquire("p")
	if loads, _ := h.counts(); loads != 2 {
		t.Fatalf("%d loads, want 2 (evicted, then reloaded)", loads)
	}
	h.clock.Advance(time.Hour) // the evicted doc's retry timer must not save again
	h.wantSaves(1, "after the evicted doc's retry time")
}

func TestLiveHeldDocsAreNotEvicted(t *testing.T) {
	h := newHarness(t)
	_, release := h.acquire("p")
	h.clock.Advance(time.Hour)
	release()
	h.acquire("p")
	if loads, _ := h.counts(); loads != 1 {
		t.Fatalf("%d loads, want 1: a held doc was evicted", loads)
	}
}

func TestLiveEvictionKeepsDocWhenSaveFails(t *testing.T) {
	h := newHarness(t)
	d, release := h.acquire("p")
	h.set(d, "k", "v")
	release()
	h.failNext = 1000
	h.clock.Advance(time.Hour)

	d, release = h.acquire("p")
	defer release()
	if loads, _ := h.counts(); loads != 1 {
		t.Fatalf("%d loads, want 1: an unsaved doc was evicted", loads)
	}
	h.set(d, "k2", "still open")
}

func TestLiveConcurrentAcquireLoadsOnce(t *testing.T) {
	h := newHarness(t)
	h.loadGate = make(chan struct{})
	var wg sync.WaitGroup
	var got atomic.Int32
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, release, err := h.reg.Acquire(context.Background(), "p")
			if err == nil && d.ID() == "p" {
				got.Add(1)
				release()
			}
		}()
	}
	time.Sleep(10 * time.Millisecond) // let them all queue behind the first load
	close(h.loadGate)
	wg.Wait()
	if loads, _ := h.counts(); loads != 1 || got.Load() != 10 {
		t.Fatalf("%d loads and %d successful acquires, want 1 and 10", loads, got.Load())
	}
}

func TestLiveFailedLoadIsNotCached(t *testing.T) {
	h := newHarness(t)
	h.store["p"] = engine.Snapshot{Format: engine.WireNative, Version: 1, Payload: []byte("garbage!")}
	if _, _, err := h.reg.Acquire(context.Background(), "p"); err == nil {
		t.Fatal("loading a corrupt snapshot succeeded")
	}
	delete(h.store, "p")
	h.acquire("p")
}

func TestLiveCloseSavesDirtyDocs(t *testing.T) {
	h := newHarness(t)
	d, release := h.acquire("p")
	defer release()
	h.set(d, "k", "unsaved")
	if err := h.reg.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := h.stored("p", "k"); got != "unsaved" {
		t.Errorf("stored k = %v after Close, want unsaved", got)
	}
	if _, _, err := h.reg.Acquire(context.Background(), "p"); !errors.Is(err, live.ErrClosed) {
		t.Errorf("Acquire after Close: got %v, want ErrClosed", err)
	}
	h.clock.Advance(time.Hour) // stopped timers must not save again
	h.wantSaves(1, "after Close")
}

// A save is told who made the last change it holds, and when.
func TestLiveSaveCarriesLastEdit(t *testing.T) {
	h := newHarness(t)
	d, release := h.acquire("p")
	defer release()
	h.set(d, "k", "by ann")
	h.clock.Advance(time.Second)
	at := h.clock.Now()
	in, _ := engine.MockDelta("", 0, engine.MockSet{Key: "k", Value: "by bob"})
	if err := d.Update(0, "bob", func(doc engine.Document) error { _, err := doc.Edit(context.Background(), in); return err }); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(2 * time.Second)
	if h.lastEdit.By != "bob" || !h.lastEdit.At.Equal(at) {
		t.Errorf("save told %+v, want bob at %v", h.lastEdit, at)
	}
}
