package wal_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/mjcourte/modulith/internal/engine"
	gnats "github.com/mjcourte/modulith/platform/nats"
	"github.com/mjcourte/modulith/services/documents/internal/wal"
)

// connect runs against the compose NATS cluster (see platform/nats/README.md).
func connect(t *testing.T) (context.Context, *gnats.Client) {
	t.Helper()
	url := os.Getenv("MODULITH_TEST_NATS_URL")
	if url == "" {
		t.Skip("set MODULITH_TEST_NATS_URL to run against NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	c, err := gnats.Connect(ctx, url, "documents-wal-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })
	return ctx, c
}

func uniqueID(prefix string) string { return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano()) }

func delta(t *testing.T, key, value string) engine.DeltaInput {
	t.Helper()
	in, err := engine.MockDelta("r1", 1, engine.MockSet{Key: key, Value: value})
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func TestPublishAndReplay(t *testing.T) {
	ctx, c := connect(t)
	log := wal.New(c.JS)
	p := uniqueID("p")
	in := wal.Delta{ProjectID: p, ID: uuid.Must(uuid.NewV7()).String(), ActorID: "ann", Origin: "r1", Delta: delta(t, "k", "v")}

	seq, err := log.PublishDelta(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	again, err := log.PublishDelta(ctx, in) // a retry of the same op
	if err != nil || again != seq {
		t.Fatalf("republish: seq %d, %v; want the original %d (deduplicated)", again, err, seq)
	}

	var got []wal.Delta
	if err := log.DeltasAfter(ctx, p, 0, func(d wal.Delta) error { got = append(got, d); return nil }, func(error) {}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("replayed %d deltas, want 1", len(got))
	}
	d := got[0]
	if d.ProjectID != p || d.ID != in.ID || d.Seq != seq || d.ActorID != "ann" || d.Origin != "r1" ||
		d.Delta.Format != in.Delta.Format || d.Delta.Version != in.Delta.Version || string(d.Delta.Payload) != string(in.Delta.Payload) {
		t.Errorf("replayed %+v, want %+v at seq %d", d, in, seq)
	}
}

func TestConsume(t *testing.T) {
	ctx, c := connect(t)
	log := wal.New(c.JS)
	p := uniqueID("p")
	durable := uniqueID("test")
	t.Cleanup(func() { c.JS.DeleteConsumer(context.Background(), gnats.Documents.Name, durable) })

	var mu sync.Mutex
	var applied []string
	var bad []error
	attempts := map[string]int{}
	cctx, stop := context.WithCancel(ctx)
	done := make(chan error)
	go func() {
		done <- log.Consume(cctx, wal.ConsumerConfig{Durable: durable, DeliverNew: true},
			func(_ context.Context, d wal.Delta) error {
				mu.Lock()
				defer mu.Unlock()
				if d.ProjectID != p {
					return nil // another test's traffic
				}
				key := fmt.Sprint(d.Seq) // not the payload: other tests publish identical bytes
				attempts[key]++
				switch {
				case d.ActorID == "poison":
					return fmt.Errorf("%w: test", wal.ErrBadDelta)
				case d.ActorID == "flaky" && attempts[key] == 1:
					return errors.New("try again")
				}
				applied = append(applied, d.ActorID)
				return nil
			},
			func(err error) {
				if strings.Contains(err.Error(), p) { // other tests' traffic shares the stream
					mu.Lock()
					bad = append(bad, err)
					mu.Unlock()
				}
			})
	}()
	waitForConsumer(t, ctx, c.JS, durable)

	for i, actor := range []string{"ann", "poison", "flaky"} {
		if _, err := log.PublishDelta(ctx, wal.Delta{ProjectID: p, ActorID: actor, Delta: delta(t, "k", fmt.Sprint(i))}); err != nil {
			t.Fatal(err)
		}
	}
	// A message without the headers can't be decoded.
	subj, _ := gnats.DocumentDelta(p)
	if _, err := c.JS.Publish(ctx, subj, []byte("raw")); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(15 * time.Second) // the flaky one waits out a 5s redelivery delay
	for {
		mu.Lock()
		n := len(applied)
		mu.Unlock()
		if n >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	stop()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(applied) != 2 || applied[0] != "ann" || applied[1] != "flaky" {
		t.Errorf("applied %v, want [ann flaky] (poison dropped, flaky redelivered)", applied)
	}
	// poison (dropped), flaky's first try, and the raw message.
	if len(bad) != 3 {
		t.Errorf("onBad heard %d failures, want 3: %v", len(bad), bad)
	}
}

// Identical edits to two projects have identical bytes; both must be stored.
func TestPublishSameBytesToTwoProjects(t *testing.T) {
	ctx, c := connect(t)
	log := wal.New(c.JS)
	in := delta(t, "k", "same")
	a, err := log.PublishDelta(ctx, wal.Delta{ProjectID: uniqueID("a"), Delta: in})
	if err != nil {
		t.Fatal(err)
	}
	b, err := log.PublishDelta(ctx, wal.Delta{ProjectID: uniqueID("b"), Delta: in})
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("both publishes returned seq %d: the second project's delta was dropped as a duplicate", a)
	}
}

// A message replay can't decode is skipped and reported, not fatal: one bad
// message must not make a project impossible to load.
func TestReplaySkipsUnreadableMessages(t *testing.T) {
	ctx, c := connect(t)
	log := wal.New(c.JS)
	p := uniqueID("p")
	subj, _ := gnats.DocumentDelta(p)
	if _, err := log.PublishDelta(ctx, wal.Delta{ProjectID: p, Delta: delta(t, "k", "before")}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.JS.Publish(ctx, subj, []byte("no headers")); err != nil {
		t.Fatal(err)
	}
	if _, err := log.PublishDelta(ctx, wal.Delta{ProjectID: p, Delta: delta(t, "k", "after")}); err != nil {
		t.Fatal(err)
	}
	var good, bad int
	err := log.DeltasAfter(ctx, p, 0, func(wal.Delta) error { good++; return nil }, func(error) { bad++ })
	if err != nil || good != 2 || bad != 1 {
		t.Errorf("replay: %d good, %d bad, %v; want 2, 1, nil", good, bad, err)
	}
}

// A delta's trace crosses NATS: the consumer applies it inside the
// publisher's trace (a browser's edit is one trace, gateway to documents).
func TestConsumeContinuesTheTrace(t *testing.T) {
	ctx, c := connect(t)
	otel.SetTracerProvider(sdktrace.NewTracerProvider())
	otel.SetTextMapPropagator(propagation.TraceContext{})
	log := wal.New(c.JS)
	p := uniqueID("p")
	durable := uniqueID("test")
	t.Cleanup(func() { c.JS.DeleteConsumer(context.Background(), gnats.Documents.Name, durable) })

	got := make(chan trace.SpanContext, 1)
	cctx, stop := context.WithCancel(ctx)
	done := make(chan error)
	go func() {
		done <- log.Consume(cctx, wal.ConsumerConfig{Durable: durable, DeliverNew: true},
			func(ctx context.Context, d wal.Delta) error {
				if d.ProjectID == p {
					got <- trace.SpanContextFromContext(ctx)
				}
				return nil
			}, func(error) {})
	}()
	waitForConsumer(t, ctx, c.JS, durable)

	pctx, edit := otel.Tracer("test").Start(ctx, "ws.edit")
	if _, err := log.PublishDelta(pctx, wal.Delta{ProjectID: p, ActorID: "ann", Delta: delta(t, "k", "v")}); err != nil {
		t.Fatal(err)
	}
	edit.End()
	select {
	case sc := <-got:
		if sc.TraceID() != edit.SpanContext().TraceID() {
			t.Errorf("applied in trace %s, want the publisher's %s", sc.TraceID(), edit.SpanContext().TraceID())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the delta wasn't consumed")
	}
	stop()
	if err := <-done; err != nil {
		t.Fatal(err)
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
