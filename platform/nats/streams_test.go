package nats_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"gyoza/platform/nats"
)

// connect runs against a real cluster (Replicas: 3 needs three servers):
//
//	docker compose -f deploy/monolith/compose.yaml up -d --wait nats-1 nats-2 nats-3
//	GYOZA_TEST_NATS_URL=nats://localhost:4222
//
// Streams are shared with anything else using the cluster, so each test uses
// fresh project IDs.
func connect(t *testing.T) (context.Context, *nats.Client) {
	t.Helper()
	url := os.Getenv("GYOZA_TEST_NATS_URL")
	if url == "" {
		t.Skip("set GYOZA_TEST_NATS_URL to run against NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	c, err := nats.Connect(ctx, url, "gyoza-test")
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

func TestSnapshotsKeepLatestPerProject(t *testing.T) {
	ctx, c := connect(t)
	p := uniqueID("snap")
	subj, _ := nats.DocumentSnapshot(p)
	publish(t, ctx, c.JS, subj, "v1")
	publish(t, ctx, c.JS, subj, "v2")

	got := remaining(t, ctx, c.JS, nats.DocumentSnapshots.Name, subj)
	if len(got) != 1 || got[0] != "v2" {
		t.Fatalf("snapshots = %q, want [v2]", got)
	}
}

func TestPurgeDeltas(t *testing.T) {
	ctx, c := connect(t)
	p, other := uniqueID("purge"), uniqueID("other")
	subj, _ := nats.DocumentDelta(p)
	otherSubj, _ := nats.DocumentDelta(other)

	// o1 sits inside the purged sequence range, so a purge that ignores the
	// subject would take it too.
	publish(t, ctx, c.JS, otherSubj, "o1")
	publish(t, ctx, c.JS, subj, "d1")
	covered := publish(t, ctx, c.JS, subj, "d2")
	publish(t, ctx, c.JS, subj, "d3")

	for range 2 { // a repeated purge must change nothing
		if err := nats.PurgeDeltas(ctx, c.JS, p, covered); err != nil {
			t.Fatal(err)
		}
	}
	if got := remaining(t, ctx, c.JS, nats.DocumentDeltas.Name, subj); len(got) != 1 || got[0] != "d3" {
		t.Errorf("project deltas = %q, want [d3]", got)
	}
	if got := remaining(t, ctx, c.JS, nats.DocumentDeltas.Name, otherSubj); len(got) != 1 || got[0] != "o1" {
		t.Errorf("other project's deltas = %q, want [o1]", got)
	}
}
