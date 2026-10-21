package nats_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/mjcourte/modulith/platform/nats"
)

// connect runs against a real cluster (Replicas: 3 needs three servers):
//
//	docker compose -f deploy/monolith/compose.yaml up -d --wait nats-1 nats-2 nats-3
//	MODULITH_TEST_NATS_URL=nats://localhost:4222
//
// Streams are shared with anything else using the cluster, so each test uses
// fresh project IDs.
func connect(t *testing.T) (context.Context, *nats.Client) {
	t.Helper()
	url := os.Getenv("MODULITH_TEST_NATS_URL")
	if url == "" {
		t.Skip("set MODULITH_TEST_NATS_URL to run against NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	c, err := nats.Connect(ctx, url, "modulith-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })
	return ctx, c
}

func uniqueID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

func publish(t *testing.T, ctx context.Context, js jetstream.JetStream, subj, data string) uint64 {
	t.Helper()
	ack, err := js.Publish(ctx, subj, []byte(data))
	if err != nil {
		t.Fatal(err)
	}
	return ack.Sequence
}

// remaining returns the payloads stored on subj, oldest first.
func remaining(t *testing.T, ctx context.Context, js jetstream.JetStream, stream, subj string) []string {
	t.Helper()
	cons, err := js.OrderedConsumer(ctx, stream, jetstream.OrderedConsumerConfig{FilterSubjects: []string{subj}})
	if err != nil {
		t.Fatal(err)
	}
	info, err := cons.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for range info.NumPending {
		msg, err := cons.Next(jetstream.FetchMaxWait(5 * time.Second))
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, string(msg.Data()))
	}
	return got
}

func TestEnsureStreamsIdempotent(t *testing.T) {
	ctx, c := connect(t) // Connect already ran EnsureStreams once.
	if err := nats.EnsureStreams(ctx, c.JS); err != nil {
		t.Fatalf("second EnsureStreams: %v", err)
	}
}

func TestCompact(t *testing.T) {
	ctx, c := connect(t)
	p, other := uniqueID("compact"), uniqueID("other")
	deltas, _ := nats.DocumentDelta(p)
	snaps, _ := nats.DocumentSnapshot(p)
	otherDeltas, _ := nats.DocumentDelta(other)

	// o1 sits inside the purged sequence range, so a purge that ignores the
	// subject would take it too.
	publish(t, ctx, c.JS, otherDeltas, "o1")
	publish(t, ctx, c.JS, snaps, "s1")
	publish(t, ctx, c.JS, deltas, "d1")
	covered := publish(t, ctx, c.JS, deltas, "d2")
	publish(t, ctx, c.JS, deltas, "d3")
	publish(t, ctx, c.JS, snaps, "s2")

	for range 2 { // repeating must change nothing
		if err := nats.Compact(ctx, c.JS, p, covered); err != nil {
			t.Fatal(err)
		}
	}
	if got := remaining(t, ctx, c.JS, nats.Documents.Name, deltas); len(got) != 1 || got[0] != "d3" {
		t.Errorf("project deltas = %q, want [d3]", got)
	}
	if got := remaining(t, ctx, c.JS, nats.Documents.Name, snaps); len(got) != 1 || got[0] != "s2" {
		t.Errorf("project snapshots = %q, want [s2]", got)
	}
	if got := remaining(t, ctx, c.JS, nats.Documents.Name, otherDeltas); len(got) != 1 || got[0] != "o1" {
		t.Errorf("other project's deltas = %q, want [o1]", got)
	}
}

func TestLatestSnapshot(t *testing.T) {
	ctx, c := connect(t)
	p := uniqueID("latest")
	if _, err := nats.LatestSnapshot(ctx, c.JS, p); !errors.Is(err, nats.ErrNoSnapshot) {
		t.Fatalf("no pointer yet: got %v, want ErrNoSnapshot", err)
	}
	snaps, _ := nats.DocumentSnapshot(p)
	publish(t, ctx, c.JS, snaps, "s1")
	want := publish(t, ctx, c.JS, snaps, "s2")

	msg, err := nats.LatestSnapshot(ctx, c.JS, p)
	if err != nil {
		t.Fatal(err)
	}
	if string(msg.Data) != "s2" || msg.Sequence != want {
		t.Errorf("latest = %q at %d, want s2 at %d", msg.Data, msg.Sequence, want)
	}
}

func TestDeltasAfter(t *testing.T) {
	ctx, c := connect(t)
	p, other := uniqueID("after"), uniqueID("other")
	deltas, _ := nats.DocumentDelta(p)
	otherDeltas, _ := nats.DocumentDelta(other)
	snaps, _ := nats.DocumentSnapshot(p)

	publish(t, ctx, c.JS, deltas, "d1")
	from := publish(t, ctx, c.JS, deltas, "d2")
	publish(t, ctx, c.JS, otherDeltas, "o1") // another project, interleaved
	publish(t, ctx, c.JS, snaps, "s1")       // same project, not a delta
	publish(t, ctx, c.JS, deltas, "d3")
	publish(t, ctx, c.JS, deltas, "d4")

	var got []string
	err := nats.DeltasAfter(ctx, c.JS, p, from, func(m *jetstream.RawStreamMsg) error {
		got = append(got, string(m.Data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"d3", "d4"}; !slices.Equal(got, want) {
		t.Errorf("deltas after %d = %q, want %q", from, got, want)
	}

	stop := errors.New("stop")
	calls := 0
	err = nats.DeltasAfter(ctx, c.JS, p, 0, func(*jetstream.RawStreamMsg) error { calls++; return stop })
	if !errors.Is(err, stop) || calls != 1 {
		t.Errorf("fn error: got %v after %d calls, want stop after 1", err, calls)
	}
}
