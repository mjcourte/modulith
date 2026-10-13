package ws_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"gyoza/internal/engine"
	gnats "gyoza/platform/nats"
	"gyoza/services/documents"
	"gyoza/services/gateway/internal/session"
	"gyoza/services/gateway/internal/ws"
	"gyoza/services/iam"
	"gyoza/services/storage"
)

// world is the monolith in memory behind the WebSocket: real iam, storage
// and documents (without NATS), and a memory bus standing in for NATS.
// Connections name their actor (and optionally the session's expiry) in
// test headers, standing in for the session.
type world struct {
	t   *testing.T
	iam *iam.Manager
	bus *memBus
	srv *httptest.Server
}

const (
	actorHeader  = "X-Test-Actor"
	expiryHeader = "X-Test-Expiry" // milliseconds since the epoch
)

func newWorld(t *testing.T) *world { return newWorldWith(t, nil) }

// newWorldWith lets a test wrap documents (to act while a join reads it).
func newWorldWith(t *testing.T, wrap func(documents.Service) documents.Service) *world {
	t.Helper()
	store := iam.NewMemoryStore()
	authz := iam.NewStorageAuthorizer(store)
	files, err := storage.NewMemory(authz, storage.Config{Bucket: "gyoza-test"})
	if err != nil {
		t.Fatal(err)
	}
	local := documents.OpenLocal(files, memTransfer{files}, authz)
	t.Cleanup(func() { local.Close(context.Background()) })
	m, err := iam.NewManager(store, files, local, iam.NoIdentityProvider{}, iam.Config{})
	if err != nil {
		t.Fatal(err)
	}
	var docs documents.Service = local
	if wrap != nil {
		docs = wrap(docs)
	}
	bus := newMemBus()
	hub := ws.New(docs, authz, bus)
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		hub.Run(ctx)
		close(done)
	}()
	<-bus.running

	upgrade := hub.Handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := session.WithActor(r.Context(), r.Header.Get(actorHeader))
		if ms, err := strconv.ParseInt(r.Header.Get(expiryHeader), 10, 64); err == nil {
			ctx = session.WithExpiry(ctx, time.UnixMilli(ms))
		}
		upgrade.ServeHTTP(w, r.WithContext(ctx))
	}))
	t.Cleanup(func() {
		stop()
		select {
		case <-done:
		case <-time.After(5 * time.Second): // a stalled hub; the test has failed already
		}
		srv.Close()
	})
	return &world{t: t, iam: m, bus: bus, srv: srv}
}

// memTransfer moves snapshot bytes through storage.Memory's fake S3.
type memTransfer struct{ m *storage.Memory }

func (t memTransfer) Put(_ context.Context, url string, h map[string]string, data []byte) error {
	return t.m.Upload(url, h["Content-Type"], data)
}

func (t memTransfer) Get(_ context.Context, url string) ([]byte, error) { return t.m.Download(url) }

// signIn creates a user as the gateway's sign-in hook would.
func (w *world) signIn(name string) string {
	w.t.Helper()
	_, err := w.iam.SignIn(context.Background(), iam.Identity{UserID: name, Email: name + "@example.com", EmailVerified: true, Name: name})
	if err != nil {
		w.t.Fatal(err)
	}
	return name
}

// project creates a project owned by owner, shared with others by access.
func (w *world) project(owner string, shares map[string]iam.Access) string {
	w.t.Helper()
	ctx := context.Background()
	p, err := w.iam.CreateProject(ctx, owner, storage.Owner{Kind: storage.OwnerUser, ID: owner}, "Poster", false)
	if err != nil {
		w.t.Fatal(err)
	}
	for user, access := range shares {
		if _, err := w.iam.ShareProject(ctx, owner, p.ID, user+"@example.com", access); err != nil {
			w.t.Fatal(err)
		}
	}
	return p.ID
}

// client is one browser tab.
type client struct {
	t       *testing.T
	conn    *websocket.Conn
	replica string
}

func (w *world) connect(actor string, header http.Header) *client {
	w.t.Helper()
	c, err := w.dial(actor, header)
	if err != nil {
		w.t.Fatal(err)
	}
	hello := c.next()
	if hello["type"] != "hello" || !strings.HasPrefix(str(hello["replica"]), actor+".") {
		w.t.Fatalf("first message: %v", hello)
	}
	if ms, _ := hello["serverTime"].(float64); time.Since(time.UnixMilli(int64(ms))).Abs() > time.Minute {
		w.t.Errorf("serverTime %v is not now", hello["serverTime"])
	}
	c.replica = str(hello["replica"])
	return c
}

func (w *world) dial(actor string, header http.Header) (*client, error) {
	if header == nil {
		header = http.Header{}
	}
	header.Set(actorHeader, actor)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(w.srv.URL, "http"), &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(-1)
	w.t.Cleanup(func() { conn.CloseNow() })
	return &client{t: w.t, conn: conn}, nil
}

func (c *client) send(msg map[string]any) {
	c.t.Helper()
	b, err := json.Marshal(msg)
	if err != nil {
		c.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.conn.Write(ctx, websocket.MessageText, b); err != nil {
		c.t.Fatal(err)
	}
}

// read returns the next message, or the error that ended the connection.
func (c *client) read() (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, b, err := c.conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		c.t.Fatalf("message isn't JSON: %s", b)
	}
	return m, nil
}

func (c *client) next() map[string]any {
	c.t.Helper()
	m, err := c.read()
	if err != nil {
		c.t.Fatal(err)
	}
	return m
}

// expect reads the next message and fails unless its type is typ.
func (c *client) expect(typ string) map[string]any {
	c.t.Helper()
	m := c.next()
	if m["type"] != typ {
		c.t.Fatalf("got %v, want a %s message", m, typ)
	}
	return m
}

// expectError reads the next message and fails unless it's an error with
// code.
func (c *client) expectError(code string) map[string]any {
	c.t.Helper()
	m := c.expect("error")
	if m["code"] != code {
		c.t.Fatalf("got %v, want error %s", m, code)
	}
	return m
}

// sync proves nothing else is queued for c: everything fanned out before it
// was queued earlier, so a client seeing this error next saw nothing else.
func (c *client) sync() {
	c.t.Helper()
	c.send(map[string]any{"type": "sync-probe"})
	c.expectError("BAD_REQUEST")
}

func (c *client) join(projectID string) map[string]any {
	c.t.Helper()
	c.send(map[string]any{"type": "join", "projectId": projectID})
	return c.expect("document")
}

// edit builds an edit stamped by replica (lamport 0: unstamped).
func edit(t *testing.T, projectID, id, replica string, lamport uint64) map[string]any {
	t.Helper()
	d, err := engine.MockDelta(replica, lamport, engine.MockSet{Key: "title", Value: "Hello"})
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"type": "edit", "projectId": projectID, "id": id,
		"format": string(d.Format), "version": d.Version, "payload": d.Payload}
}

// closeStatus is the close code that ended a connection, reading past
// anything still queued.
func (c *client) closeStatus() websocket.StatusCode {
	c.t.Helper()
	for {
		_, err := c.read()
		if err != nil {
			return websocket.CloseStatus(err)
		}
	}
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// memBus is NATS in memory: published deltas get sequence numbers and come
// back through the feed, as the stream does.
type memBus struct {
	running chan struct{}

	mu        sync.Mutex
	feed      ws.Feed
	seq       uint64
	published []gnats.Delta
	ids       map[string]uint64
}

func newMemBus() *memBus {
	return &memBus{running: make(chan struct{}), ids: map[string]uint64{}}
}

func (b *memBus) PublishDelta(_ context.Context, d gnats.Delta) (uint64, error) {
	b.mu.Lock()
	if seq, ok := b.ids[d.ID]; ok {
		b.mu.Unlock()
		return seq, nil // the stream's dedupe
	}
	b.seq++
	d.Seq = b.seq
	b.ids[d.ID] = d.Seq
	b.published = append(b.published, d)
	feed := b.feed
	b.mu.Unlock()
	feed.Delta(d)
	return d.Seq, nil
}

func (b *memBus) PublishUserState(_ context.Context, u ws.UserState) error {
	b.mu.Lock()
	feed := b.feed
	b.mu.Unlock()
	feed.UserState(u)
	return nil
}

func (b *memBus) Run(ctx context.Context, f ws.Feed) error {
	b.mu.Lock()
	b.feed = f
	b.mu.Unlock()
	close(b.running)
	<-ctx.Done()
	return nil
}

// inject delivers a delta as if documents had published it.
func (b *memBus) inject(projectID string, seq uint64, payload string) {
	b.mu.Lock()
	feed := b.feed
	b.mu.Unlock()
	feed.Delta(gnats.Delta{ProjectID: projectID, ID: "m" + strconv.FormatUint(seq, 10), Seq: seq, ActorID: "docs",
		Origin: "docs-1", Format: "native", Version: 1, Payload: []byte(payload)})
}

func (b *memBus) snapshot(projectID string, p gnats.Pointer) {
	b.mu.Lock()
	feed := b.feed
	b.mu.Unlock()
	feed.Snapshot(projectID, p)
}

func (b *memBus) deltas() []gnats.Delta {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]gnats.Delta(nil), b.published...)
}
