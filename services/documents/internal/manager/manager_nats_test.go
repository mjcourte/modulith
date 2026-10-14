package manager_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"gyoza/internal/engine"
	gnats "gyoza/platform/nats"
	"gyoza/services/documents/internal/live"
	"gyoza/services/documents/internal/manager"
	"gyoza/services/documents/internal/snapshots"
	"gyoza/services/documents/internal/wal"
)

// natsWorld is several documents replicas sharing one snapshot store, one
// usage table and the real DOCUMENTS stream (compose cluster, see
// platform/nats/README.md). Each test uses fresh project IDs.
type natsWorld struct {
	t     *testing.T
	ctx   context.Context
	nc    *gnats.Client
	log   *wal.Log
	store *snapshots.Memory
	uses  *fakeUses
	authz fakeAuthz

	// ours is every project this test made. Its consumers ignore the rest:
	// other packages' tests share the stream, and applying (then saving and
	// compacting) their projects would delete messages they're waiting for.
	ownMu sync.Mutex
	ours  map[string]bool
}

func newNATSWorld(t *testing.T) *natsWorld {
	url := os.Getenv("GYOZA_TEST_NATS_URL")
	if url == "" {
		t.Skip("set GYOZA_TEST_NATS_URL to run against NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	nc, err := gnats.Connect(ctx, url, "documents-manager-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { nc.Close(context.Background()) })
	return &natsWorld{
		t: t, ctx: ctx, nc: nc, log: wal.New(nc.JS),
		store: snapshots.NewDurableMemory(), // stands in for S3, so saves compact
		ours:  map[string]bool{},
		uses:  &fakeUses{rows: map[string][]engine.AssetRef{}},
		authz: fakeAuthz{readers: map[string]bool{"rita": true}, writers: map[string]bool{"ann": true}},
	}
}

// replica starts a Manager named replica. With consume, it also runs its own
// durable consumer (new messages only), stopped and deleted at cleanup. The
// long debounce keeps saves out of the way unless a test closes it.
func (w *natsWorld) replica(name string, consume bool) *manager.Manager {
	m := manager.New(engine.Mock{}, w.store, w.authz, w.uses, manager.Config{
		Replica: name,
		Live:    live.Config{Debounce: time.Hour, MaxDelay: time.Hour},
		WAL:     w.log,
	})
	w.t.Cleanup(func() { m.Close(context.Background()) })
	if consume {
		durable := fmt.Sprintf("test-%s-%d", name, time.Now().UnixNano())
		ctx, stop := context.WithCancel(w.ctx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			apply := func(ctx context.Context, d wal.Delta) error {
				if !w.isOurs(d.ProjectID) {
					return nil
				}
				return m.ApplyDelta(ctx, d)
			}
			err := w.log.Consume(ctx, wal.ConsumerConfig{Durable: durable, DeliverNew: true}, apply,
				func(err error) { slog.Info("test consumer", "replica", name, "err", err) })
			if err != nil {
				w.t.Error(err)
			}
		}()
		w.t.Cleanup(func() {
			stop()
			<-done
			w.nc.JS.DeleteConsumer(context.Background(), gnats.Documents.Name, durable)
		})
		waitForConsumer(w.t, w.ctx, w.nc.JS, durable)
	}
	return m
}

func (w *natsWorld) project() string {
	p := fmt.Sprintf("p-%d", time.Now().UnixNano())
	w.ownMu.Lock()
	w.ours[p] = true
	w.ownMu.Unlock()
	return p
}

func (w *natsWorld) isOurs(p string) bool {
	w.ownMu.Lock()
	defer w.ownMu.Unlock()
	return w.ours[p]
}

func editOn(t *testing.T, m *manager.Manager, project string, sets ...engine.MockSet) engine.Delta {
	t.Helper()
	in, err := engine.MockDelta("", 0, sets...)
	if err != nil {
		t.Fatal(err)
	}
	out, err := m.UpdateDocument(context.Background(), "ann", project, in)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func valueOn(t *testing.T, m *manager.Manager, project, key string) any {
	t.Helper()
	d, err := m.GetDocument(context.Background(), "rita", project)
	if err != nil {
		t.Fatal(err)
	}
	return registers(t, d)[key]
}

// eventually polls cond for up to 10s.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !cond(); {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// An edit on one replica reaches another through the stream.
func TestNATSReplicasConverge(t *testing.T) {
	w := newNATSWorld(t)
	r1, r2 := w.replica("r1", true), w.replica("r2", true)
	p := w.project()
	valueOn(t, r2, p, "k") // r2 has it open before the edit

	editOn(t, r1, p, engine.MockSet{Key: "k", Value: "from r1"})
	eventually(t, "r2 to apply r1's edit", func() bool { return valueOn(t, r2, p, "k") == "from r1" })
}

// A replica that crashed before saving loses nothing: the next one loads the
// last snapshot and replays the stream after it.
func TestNATSReplayAfterCrash(t *testing.T) {
	w := newNATSWorld(t)
	crashed := w.replica("crashed", false)
	p := w.project()
	editOn(t, crashed, p, engine.MockSet{Key: "k", Value: "unsaved"})
	editOn(t, crashed, p, engine.MockSet{Key: "k2", Value: "also unsaved"})
	// No Close: the debounce is an hour, so nothing past version 1 was stored.
	if ref, _ := w.store.Latest(w.ctx, p); ref.Version != 1 {
		t.Fatalf("latest snapshot is version %d, want 1 (only the empty first one)", ref.Version)
	}

	next := w.replica("next", false)
	if got := valueOn(t, next, p, "k"); got != "unsaved" {
		t.Errorf("after replay k = %v, want unsaved", got)
	}
	if got := valueOn(t, next, p, "k2"); got != "also unsaved" {
		t.Errorf("after replay k2 = %v, want \"also unsaved\"", got)
	}
}

// Saving stores a snapshot, announces it, then compacts what it covers.
func TestNATSSaveAnnouncesAndCompacts(t *testing.T) {
	w := newNATSWorld(t)
	m := w.replica("r1", true)
	p := w.project()
	editOn(t, m, p, engine.MockSet{Key: "k", Value: "v1"})
	editOn(t, m, p, engine.MockSet{Key: "k", Value: "v2"})
	// The document covers its own edits once their echoes come back through
	// the consumer: only then does its sequence reach theirs.
	var last uint64
	w.log.DeltasAfter(w.ctx, p, 0, func(d wal.Delta) error { last = d.Seq; return nil }, func(error) {})
	eventually(t, "the echoes to be applied", func() bool {
		d, err := m.GetDocument(w.ctx, "rita", p)
		return err == nil && d.Seq == last
	})
	if err := m.Close(w.ctx); err != nil { // saves
		t.Fatal(err)
	}

	ref, err := w.store.Latest(w.ctx, p)
	if err != nil || ref.Version != 2 || ref.Seq == 0 {
		t.Fatalf("latest snapshot %+v, %v; want version 2 covering the edits", ref, err)
	}
	msg, err := gnats.LatestSnapshot(w.ctx, w.nc.JS, p)
	if err != nil {
		t.Fatal(err)
	}
	var ptr wal.Pointer
	if err := json.Unmarshal(msg.Data, &ptr); err != nil {
		t.Fatal(err)
	}
	if ptr.Version != ref.Version || ptr.Seq != ref.Seq || ptr.ObjectID != ref.ObjectID || ptr.EditedBy != "ann" || !ptr.EditedAt.Equal(ref.EditedAt) {
		t.Errorf("pointer %+v, want it to match the stored %+v (edited by ann)", ptr, ref)
	}
	n := 0
	w.log.DeltasAfter(w.ctx, p, 0, func(wal.Delta) error { n++; return nil }, func(error) {})
	if n != 0 {
		t.Errorf("%d deltas left after compaction, want 0", n)
	}
}

// A delta the gateway publishes (with its actor) is applied by the consumer,
// which also records the asset uses it adds.
func TestNATSGatewayDeltaSyncsUses(t *testing.T) {
	w := newNATSWorld(t)
	m := w.replica("r1", true)
	p := w.project()
	x1 := engine.AssetRef{ID: "x", Version: 1}
	in, err := engine.MockDelta("browser-1", 5, engine.MockSet{Key: "bg", Value: x1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.log.PublishDelta(w.ctx, wal.Delta{ProjectID: p, ActorID: "ann", Delta: in}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the consumer to apply the gateway's delta", func() bool {
		return slices.Equal(w.uses.of(p), []engine.AssetRef{x1})
	})
	if got := valueOn(t, m, p, "bg"); got == nil {
		t.Error("bg not set on the replica")
	}
}

// Over a store that isn't durable (RAM), saving must not compact: a crash
// would lose the snapshot, so the deltas are the only copy.
func TestNATSNoCompactionWithoutDurableStore(t *testing.T) {
	w := newNATSWorld(t)
	w.store = snapshots.NewMemory()
	m := w.replica("r1", true)
	p := w.project()
	editOn(t, m, p, engine.MockSet{Key: "k", Value: "only in the stream"})
	var last uint64
	w.log.DeltasAfter(w.ctx, p, 0, func(d wal.Delta) error { last = d.Seq; return nil }, func(error) {})
	eventually(t, "the echo to be applied", func() bool {
		d, err := m.GetDocument(w.ctx, "rita", p)
		return err == nil && d.Seq == last
	})
	if err := m.Close(w.ctx); err != nil { // saves to RAM
		t.Fatal(err)
	}

	// The process "crashes": RAM, the store included, is gone. The stream
	// alone rebuilds the edit.
	w.store = snapshots.NewMemory()
	next := w.replica("next", false)
	if got := valueOn(t, next, p, "k"); got != "only in the stream" {
		t.Errorf("after a crash k = %v, want the edit rebuilt from the deltas", got)
	}
}


// waitForConsumer waits until the durable consumer has a cluster leader. A
// DeliverNew consumer fixes where it starts only once its leader is elected
// (about 60ms after it exists, longer under load): a message published before
// that is skipped. A fixed sleep lost that race when the whole suite ran.
func waitForConsumer(t *testing.T, ctx context.Context, js jetstream.JetStream, durable string) {
	t.Helper()
	led := func() bool {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second) // a slow API call can't outlast the deadline
		defer cancel()
		cons, err := js.Consumer(ctx, gnats.Documents.Name, durable)
		if err != nil {
			return false
		}
		info, err := cons.Info(ctx)
		return err == nil && info.Cluster != nil && info.Cluster.Leader != ""
	}
	for deadline := time.Now().Add(10 * time.Second); !led(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("consumer %s never started", durable)
		}
	}
}
