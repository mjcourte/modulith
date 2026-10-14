package ws_test

import (
	"context"
	"encoding/base64"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/coder/websocket"

	gnats "gyoza/platform/nats"
	"gyoza/services/documents"
	"gyoza/services/gateway/internal/ws"
	"gyoza/services/iam"
)

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// A join answers with the document, then relays the project's live traffic.
func TestJoinThenLiveTraffic(t *testing.T) {
	w := newWorld(t)
	alice := w.signIn("alice")
	p := w.project(alice, nil)
	c := w.connect(alice, nil)

	doc := c.join(p)
	if doc["projectId"] != p || doc["format"] != "native" || str(doc["payload"]) == "" {
		t.Fatalf("document: %v", doc)
	}
	w.bus.inject(p, 10, "d10")
	if d := c.expect("delta"); d["seq"] != 10.0 || d["payload"] != b64("d10") || d["actorId"] != "docs" || d["projectId"] != p {
		t.Errorf("delta: %v", d)
	}
	w.bus.snapshot(p, gnats.Pointer{Version: 3, Seq: 10, ObjectID: "obj-1"})
	if s := c.expect("snapshot"); s["objectId"] != "obj-1" || s["version"] != 3.0 || s["seq"] != 10.0 {
		t.Errorf("snapshot: %v", s)
	}
	// Other projects' traffic isn't sent.
	w.bus.inject("other", 11, "x")
	c.sync()
}

// gate runs during while a join reads the document, which then claims to
// cover the stream up to seq.
type gate struct {
	documents.Service
	during func()
	seq    uint64
}

func (g gate) GetDocument(ctx context.Context, actorID, projectID string) (documents.Document, error) {
	d, err := g.Service.GetDocument(ctx, actorID, projectID)
	g.during()
	d.Seq = g.seq
	return d, err
}

// Traffic published while a join reads the document is held back until the
// document is sent, and what the document already covers is dropped: the
// client sees the document first and nothing twice.
func TestJoinBuffersUntilDocument(t *testing.T) {
	var w *world
	var p string
	w = newWorldWith(t, func(d documents.Service) documents.Service {
		return gate{Service: d, seq: 5, during: func() {
			w.bus.inject(p, 4, "covered")
			w.bus.inject(p, 6, "newer")
		}}
	})
	alice := w.signIn("alice")
	p = w.project(alice, nil)
	c := w.connect(alice, nil)

	if doc := c.join(p); doc["seq"] != 5.0 {
		t.Fatalf("document: %v", doc)
	}
	if d := c.expect("delta"); d["seq"] != 6.0 {
		t.Errorf("got delta %v, want only seq 6", d)
	}
	c.sync()
}

// Joining needs read access; a refused join receives nothing.
func TestJoinRefusedForStranger(t *testing.T) {
	w := newWorld(t)
	alice, bob := w.signIn("alice"), w.signIn("bob")
	p := w.project(alice, nil)
	c := w.connect(bob, nil)

	c.send(map[string]any{"type": "join", "projectId": p})
	if e := c.expectError("FORBIDDEN"); e["projectId"] != p {
		t.Errorf("error: %v", e)
	}
	w.bus.inject(p, 1, "secret")
	c.sync()
}

// Access is re-checked while joined: an unshared user stops receiving the
// project's traffic (and is told), while those who keep access don't notice.
func TestRevokedAccessLeaves(t *testing.T) {
	w := newWorld(t)
	w.hub.SetRecheck(20 * time.Millisecond)
	alice, bob := w.signIn("alice"), w.signIn("bob")
	p := w.project(alice, map[string]iam.Access{bob: iam.AccessViewer})
	a, b := w.connect(alice, nil), w.connect(bob, nil)
	a.join(p)
	b.join(p)

	if err := w.iam.Unshare(context.Background(), alice, p, bob); err != nil {
		t.Fatal(err)
	}
	if e := b.expectError("FORBIDDEN"); e["projectId"] != p {
		t.Errorf("error: %v", e)
	}
	w.bus.inject(p, 1, "secret")
	b.sync()
	if d := a.expect("delta"); d["payload"] != b64("secret") {
		t.Errorf("owner's delta: %v", d)
	}
	b.send(map[string]any{"type": "userstate", "projectId": p, "kind": "viewing"})
	b.expectError("NOT_JOINED")
}

func TestLeaveStopsTraffic(t *testing.T) {
	w := newWorld(t)
	alice := w.signIn("alice")
	p := w.project(alice, nil)
	c := w.connect(alice, nil)
	c.join(p)

	c.send(map[string]any{"type": "leave", "projectId": p})
	c.sync() // the leave is handled
	w.bus.inject(p, 1, "d1")
	c.sync()
	c.send(edit(t, p, "e1", c.replica, uint64(time.Now().UnixMilli())<<16))
	c.expectError("NOT_JOINED")
}

// An edit is published only when the socket joined, may write, and stamped
// it with its own replica. It's published as the session's user, never with
// an origin (whose stamps documents would trust), and comes back to every
// joined socket, the sender's own included.
func TestEdit(t *testing.T) {
	w := newWorld(t)
	alice, bob := w.signIn("alice"), w.signIn("bob")
	p := w.project(alice, map[string]iam.Access{bob: iam.AccessViewer})
	a, b := w.connect(alice, nil), w.connect(alice, nil) // two tabs
	viewer := w.connect(bob, nil)
	now := uint64(time.Now().UnixMilli()) << 16

	a.send(edit(t, p, "e0", a.replica, now))
	a.expectError("NOT_JOINED")
	a.join(p)
	b.join(p)
	viewer.join(p)

	viewer.send(edit(t, p, "e1", viewer.replica, now))
	if e := viewer.expectError("FORBIDDEN"); e["id"] != "e1" {
		t.Errorf("error: %v", e)
	}
	a.send(edit(t, p, "e2", b.replica, now)) // the other tab's replica
	a.expectError("WRONG_REPLICA")
	a.send(edit(t, p, "e3", "", 0)) // unstamped
	a.expectError("WRONG_REPLICA")
	bad := edit(t, p, "e4", a.replica, now)
	bad["version"] = 99
	a.send(bad)
	a.expectError("UNSUPPORTED_FORMAT")
	if got := w.bus.deltas(); len(got) != 0 {
		t.Fatalf("refused edits were published: %+v", got)
	}

	ok := edit(t, p, "e5", a.replica, now)
	a.send(ok)
	want := base64.StdEncoding.EncodeToString(ok["payload"].([]byte))
	// The fan-out is queued before the ack.
	if d := a.expect("delta"); d["id"] != "e5" || d["actorId"] != alice || d["payload"] != want {
		t.Errorf("own delta: %v", d)
	}
	if ack := a.expect("ack"); ack["id"] != "e5" || ack["seq"] != 1.0 {
		t.Errorf("ack: %v", ack)
	}
	for _, c := range []*client{b, viewer} {
		if d := c.expect("delta"); d["id"] != "e5" || d["payload"] != want {
			t.Errorf("relayed delta: %v", d)
		}
	}
	got := w.bus.deltas()
	if len(got) != 1 || got[0].ActorID != alice || got[0].Origin != "" || got[0].ID != "e5" || got[0].ProjectID != p {
		t.Errorf("published: %+v", got)
	}
}

// User state is published as the session's user (a userId the client sends
// is ignored) and relayed to everyone in the project.
func TestUserState(t *testing.T) {
	w := newWorld(t)
	alice, bob := w.signIn("alice"), w.signIn("bob")
	p := w.project(alice, map[string]iam.Access{bob: iam.AccessViewer})
	a, b := w.connect(alice, nil), w.connect(bob, nil)
	a.join(p)

	b.send(map[string]any{"type": "userstate", "projectId": p, "kind": "cursorpos", "data": map[string]any{"x": 1}})
	b.expectError("NOT_JOINED")
	b.join(p)
	b.send(map[string]any{"type": "userstate", "projectId": p, "kind": "cursorpos", "userId": alice, "data": map[string]any{"x": 1}})
	for _, c := range []*client{a, b} {
		u := c.expect("userstate")
		data, _ := u["data"].(map[string]any)
		if u["userId"] != bob || u["kind"] != "cursorpos" || data["x"] != 1.0 {
			t.Errorf("userstate: %v", u)
		}
	}
	b.send(map[string]any{"type": "userstate", "projectId": p, "kind": "typing"})
	b.expectError("BAD_REQUEST")
}

// A socket that stops reading is closed; the others keep receiving.
func TestSlowSocketClosed(t *testing.T) {
	w := newWorld(t)
	alice := w.signIn("alice")
	p := w.project(alice, nil)
	slow, fast := w.connect(alice, nil), w.connect(alice, nil)
	slow.join(p)
	fast.join(p)

	big := string(make([]byte, 64<<10))
	n := 2 * 256 // twice the send queue, far past the network buffers
	// One at a time, as fast as the fast socket reads: only slow falls behind.
	// The fan-out runs aside, so a fan-out that blocks on slow fails the test
	// instead of hanging it.
	for i := 1; i <= n; i++ {
		injected := make(chan struct{})
		go func() {
			w.bus.inject(p, uint64(i), big)
			close(injected)
		}()
		select {
		case <-injected:
		case <-time.After(5 * time.Second):
			t.Fatalf("fan-out of delta %d stalled behind the slow socket", i)
		}
		if d := fast.expect("delta"); d["seq"] != float64(i) {
			t.Fatalf("fast socket: got seq %v, want %d", d["seq"], i)
		}
	}
	got := 0
	for {
		if _, err := slow.read(); err != nil {
			break
		}
		got++
	}
	if got >= n {
		t.Errorf("slow socket received all %d messages, want it closed", got)
	}
}

func TestSessionExpiryCloses(t *testing.T) {
	w := newWorld(t)
	alice := w.signIn("alice")
	exp := time.Now().Add(200 * time.Millisecond).UnixMilli()
	c := w.connect(alice, http.Header{expiryHeader: {strconv.FormatInt(exp, 10)}})
	if got := c.closeStatus(); got != ws.CloseSessionExpired {
		t.Errorf("closed with %d, want %d", got, ws.CloseSessionExpired)
	}
}

// Only same-origin pages may open a socket: the session cookie would
// otherwise let any site act as the user.
func TestCrossOriginRefused(t *testing.T) {
	w := newWorld(t)
	alice := w.signIn("alice")
	if _, err := w.dial(alice, http.Header{"Origin": {"https://evil.example"}}); err == nil {
		t.Fatal("cross-origin socket accepted")
	}
	if _, err := w.dial(alice, http.Header{"Origin": {w.srv.URL}}); err != nil {
		t.Fatalf("same-origin socket refused: %v", err)
	}
}

func TestMessagesMustBeJSON(t *testing.T) {
	w := newWorld(t)
	c := w.connect(w.signIn("alice"), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.conn.Write(ctx, websocket.MessageText, []byte("{not json"))
	c.expectError("BAD_REQUEST")
	c.conn.Write(ctx, websocket.MessageBinary, []byte("{}"))
	c.expectError("BAD_REQUEST")
}
