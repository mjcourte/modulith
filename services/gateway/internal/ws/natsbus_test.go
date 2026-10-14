package ws_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	gnats "gyoza/platform/nats"
	"gyoza/services/gateway/internal/ws"
)

// recFeed records what a Bus delivers, for one project.
type recFeed struct {
	project string
	deltas  chan gnats.Delta
	ptrs    chan gnats.Pointer
	states  chan ws.UserState
}

func (f recFeed) Delta(d gnats.Delta) {
	if d.ProjectID == f.project {
		f.deltas <- d
	}
}

func (f recFeed) Snapshot(projectID string, p gnats.Pointer) {
	if projectID == f.project {
		f.ptrs <- p
	}
}

func (f recFeed) UserState(u ws.UserState) {
	if u.ProjectID == f.project {
		f.states <- u
	}
}

func recv[T any](t *testing.T, ch chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("nothing delivered")
		panic("unreachable")
	}
}

// The NATS Bus against a real cluster: documents' deltas and pointers (as
// documents publishes them) and user state reach the feed, and the gateway's
// own deltas go out without an origin.
//
//	GYOZA_TEST_NATS_URL=nats://localhost:4222
func TestNATSBus(t *testing.T) {
	url := os.Getenv("GYOZA_TEST_NATS_URL")
	if url == "" {
		t.Skip("set GYOZA_TEST_NATS_URL to run against NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	nc, err := gnats.Connect(ctx, url, "ws-test")
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close(context.Background())

	p := "wstest-" + uuid.NewString()
	feed := recFeed{project: p, deltas: make(chan gnats.Delta, 8), ptrs: make(chan gnats.Pointer, 8), states: make(chan ws.UserState, 8)}
	bus := ws.NewNATSBus(nc)
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- bus.Run(runCtx, feed) }()
	defer func() {
		stop()
		<-done
	}()
	time.Sleep(300 * time.Millisecond) // let the consumer start (it delivers new messages only)

	// documents' delta, with its origin.
	msg, err := gnats.Delta{ProjectID: p, ID: uuid.NewString(), ActorID: "ann", Origin: "docs-1", Format: "native", Version: 1, Payload: []byte("x")}.Msg(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ack, err := nc.JS.PublishMsg(ctx, msg)
	if err != nil {
		t.Fatal(err)
	}
	if d := recv(t, feed.deltas); d.Seq != ack.Sequence || d.Origin != "docs-1" || d.ActorID != "ann" || string(d.Payload) != "x" {
		t.Errorf("documents' delta: %+v", d)
	}

	// The gateway's: no origin.
	seq, err := bus.PublishDelta(ctx, gnats.Delta{ProjectID: p, ID: uuid.NewString(), ActorID: "bob", Format: "native", Version: 1, Payload: []byte("y")})
	if err != nil {
		t.Fatal(err)
	}
	if d := recv(t, feed.deltas); d.Seq != seq || d.Origin != "" || d.ActorID != "bob" {
		t.Errorf("gateway's delta: %+v", d)
	}

	subj, _ := gnats.DocumentSnapshot(p)
	ptr, _ := json.Marshal(gnats.Pointer{Version: 2, Seq: seq, ObjectID: "obj"})
	if _, err := nc.JS.Publish(ctx, subj, ptr); err != nil {
		t.Fatal(err)
	}
	if got := recv(t, feed.ptrs); got.ObjectID != "obj" || got.Seq != seq || got.Version != 2 {
		t.Errorf("pointer: %+v", got)
	}

	if err := bus.PublishUserState(ctx, ws.UserState{ProjectID: p, UserID: "bob", Kind: "viewing", Data: json.RawMessage(`{"on":true}`)}); err != nil {
		t.Fatal(err)
	}
	if u := recv(t, feed.states); u.UserID != "bob" || u.Kind != "viewing" || string(u.Data) != `{"on":true}` {
		t.Errorf("user state: %+v", u)
	}
}
