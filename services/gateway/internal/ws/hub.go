// Package ws is the gateway's WebSocket: browsers join projects to receive
// their live document traffic and send edits and user state. One Hub per
// gateway instance reads every project's traffic from one Bus feed (one NATS
// consumer, never one per socket) and fans it out to the sockets that joined
// the project.
package ws

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	gnats "github.com/mjcourte/modulith/platform/nats"
	"github.com/mjcourte/modulith/services/documents"
	"github.com/mjcourte/modulith/services/gateway/internal/session"
	"github.com/mjcourte/modulith/services/storage"
)

// UserState is one user's presence message in a project.
type UserState struct {
	ProjectID string
	UserID    string
	Kind      string // gnats.UserStateKindViewing or UserStateKindCursor
	Data      json.RawMessage
}

// Bus is the hub's view of NATS (NATSBus); tests use a memory one.
type Bus interface {
	// PublishDelta publishes to document.{p}.delta and returns the stream
	// sequence. A repeated ID is stored once (the stream's dedupe window).
	PublishDelta(ctx context.Context, d gnats.Delta) (uint64, error)
	PublishUserState(ctx context.Context, u UserState) error
	// Run delivers every project's new deltas, snapshot pointers and user
	// state to f until ctx ends. f's methods may be called concurrently.
	Run(ctx context.Context, f Feed) error
}

// Feed receives the Bus's traffic. *Hub implements it.
type Feed interface {
	Delta(d gnats.Delta)
	Snapshot(projectID string, p gnats.Pointer)
	UserState(u UserState)
}

// Limits.
const (
	// sendQueue is how many messages a socket may fall behind (and how
	// many a joining socket buffers) before it's closed with CloseTooSlow.
	sendQueue    = 256
	maxMessage   = 1 << 20 // a client frame; deltas are at most 256 KiB, base64 adds a third
	maxUserState = 4 << 10 // a userstate message's data
	maxEditID    = 64
	// recheck is how often a socket re-checks read access to its joined
	// projects, so a revoked share or membership stops the traffic.
	recheck      = time.Minute
	writeTimeout = 10 * time.Second
	pingInterval = 30 * time.Second
	retryFeed    = 2 * time.Second
)

// Close codes: 4000-4999 are the application's.
const (
	// CloseSessionExpired: refresh the session (/auth/session/refresh),
	// reconnect and rejoin.
	CloseSessionExpired websocket.StatusCode = 4001
	// CloseTooSlow: the socket fell sendQueue messages behind. Reconnect
	// and rejoin (each join resends the document).
	CloseTooSlow websocket.StatusCode = 4008
)

// Hub tracks which sockets joined which project. Lock order: Hub.mu, then a
// socket's mu; never the other way round.
type Hub struct {
	docs  documents.Service
	authz storage.Authorizer
	bus   Bus
	now   func() time.Time
	// recheck is the access re-check interval (the recheck constant;
	// tests shorten it).
	recheck time.Duration

	mu       sync.RWMutex
	sockets  map[*socket]struct{}
	projects map[string]map[*socket]struct{}
}

var _ Feed = (*Hub)(nil)

// New returns a Hub. Run must be running for sockets to receive live
// traffic.
func New(docs documents.Service, authz storage.Authorizer, bus Bus) *Hub {
	return &Hub{docs: docs, authz: authz, bus: bus, now: time.Now, recheck: recheck,
		sockets: map[*socket]struct{}{}, projects: map[string]map[*socket]struct{}{}}
}

// Run feeds the bus's traffic to the sockets until ctx ends, then closes
// every socket (going away: the client reconnects, to another instance if
// this one is shutting down). A feed that fails to start (NATS unreachable)
// is retried.
func (h *Hub) Run(ctx context.Context) {
	defer h.observe()()
	for {
		err := h.bus.Run(ctx, h)
		if ctx.Err() != nil {
			break
		}
		slog.Error("ws: feed stopped, retrying", "err", err)
		select {
		case <-ctx.Done():
		case <-time.After(retryFeed):
		}
	}
	h.mu.RLock()
	for s := range h.sockets {
		s.close(websocket.StatusGoingAway, "server shutting down")
	}
	h.mu.RUnlock()
}

// Handler upgrades a signed-in request (session.Actor) to a socket and
// serves it until either side closes. Only same-origin pages may connect
// (websocket.Accept checks Origin against Host), matching the REST API's
// CSRF rule.
func (h *Hub) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor := session.Actor(r.Context())
		if actor == "" {
			http.Error(w, "no session", http.StatusUnauthorized)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return // Accept has answered
		}
		conn.SetReadLimit(maxMessage)
		h.serve(r.Context(), conn, actor)
	})
}

func (h *Hub) serve(ctx context.Context, conn *websocket.Conn, actor string) {
	ctx, kill := context.WithCancelCause(ctx)
	s := &socket{
		hub: h, conn: conn, actor: actor,
		// Unique per socket, so each tab is its own replica. 13 base32
		// characters are 65 random bits.
		replica: actor + "." + rand.Text()[:13],
		ctx:     ctx, kill: kill,
		out:    make(chan []byte, sendQueue),
		joined: map[string]*pending{},
	}
	h.mu.Lock()
	h.sockets[s] = struct{}{}
	h.mu.Unlock()
	defer h.drop(s)

	if exp, ok := session.Expiry(ctx); ok {
		t := time.AfterFunc(time.Until(exp), func() { s.close(CloseSessionExpired, "session expired") })
		defer t.Stop()
	}
	go s.recheckLoop(h.recheck)
	s.send(helloMsg{Type: "hello", Replica: s.replica, ServerTime: h.now().UnixMilli()})

	written := make(chan struct{})
	go func() {
		s.writeLoop()
		close(written)
	}()
	s.readLoop()
	s.kill(errClientGone)
	<-written
}

var errClientGone = errors.New("ws: client gone")

// add and remove (un)register a socket for a project's traffic.
func (h *Hub) add(projectID string, s *socket) {
	h.mu.Lock()
	defer h.mu.Unlock()
	subs := h.projects[projectID]
	if subs == nil {
		subs = map[*socket]struct{}{}
		h.projects[projectID] = subs
	}
	subs[s] = struct{}{}
}

func (h *Hub) remove(projectID string, s *socket) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.removeLocked(projectID, s)
}

func (h *Hub) removeLocked(projectID string, s *socket) {
	subs := h.projects[projectID]
	delete(subs, s)
	if len(subs) == 0 {
		delete(h.projects, projectID)
	}
}

// drop forgets a closed socket.
func (h *Hub) drop(s *socket) {
	s.mu.Lock()
	projects := make([]string, 0, len(s.joined))
	for p := range s.joined {
		projects = append(projects, p)
	}
	clear(s.joined)
	s.mu.Unlock()

	h.mu.Lock()
	defer h.mu.Unlock()
	for _, p := range projects {
		h.removeLocked(p, s)
	}
	delete(h.sockets, s)
}

// Delta, Snapshot and UserState fan the bus's traffic out.

func (h *Hub) Delta(d gnats.Delta) {
	h.fanout(d.ProjectID, d.Seq, func() any {
		return deltaMsg{Type: "delta", ProjectID: d.ProjectID, ID: d.ID, Seq: d.Seq, ActorID: d.ActorID,
			Format: d.Format, Version: d.Version, Payload: d.Payload}
	})
}

func (h *Hub) Snapshot(projectID string, p gnats.Pointer) {
	h.fanout(projectID, p.Seq, func() any {
		return snapshotMsg{Type: "snapshot", ProjectID: projectID, Version: p.Version, Seq: p.Seq,
			ObjectID: p.ObjectID, EditedBy: p.EditedBy, EditedAt: p.EditedAt}
	})
}

func (h *Hub) UserState(u UserState) {
	h.fanout(u.ProjectID, 0, func() any {
		return userStateMsg{Type: "userstate", ProjectID: u.ProjectID, UserID: u.UserID, Kind: u.Kind, Data: u.Data}
	})
}

// fanout encodes msg once, only if anyone joined the project, and queues
// the same bytes to every socket that did. seq is the delta sequence the
// message covers (0: none), so a joining socket can drop what its document
// already holds.
func (h *Hub) fanout(projectID string, seq uint64, msg func() any) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	subs := h.projects[projectID]
	if len(subs) == 0 {
		return
	}
	b, err := json.Marshal(msg())
	if err != nil {
		slog.Error("ws: encode message", "project", projectID, "err", err)
		return
	}
	for s := range subs {
		s.deliver(projectID, seq, b)
	}
}

var (
	meter = otel.Meter("modulith/services/gateway/internal/ws")
	// messages counts client messages by type, presence included: the
	// busiest message, which gets no spans.
	messages, _ = meter.Int64Counter("modulith.ws.messages",
		metric.WithDescription("Messages received from browsers, by type."), metric.WithUnit("{message}"))
)

// messageTypes are the types messages are counted under; anything else is
// "unknown", so a client can't create metric series at will.
var messageTypes = map[string]bool{"join": true, "leave": true, "edit": true, "userstate": true}

func countMessage(ctx context.Context, typ string) {
	if !messageTypes[typ] {
		typ = "unknown"
	}
	messages.Add(ctx, 1, metric.WithAttributes(attribute.String("type", typ)))
}

// observe reports the open sockets and the projects they joined at each
// collection, until the returned function stops it.
func (h *Hub) observe() func() {
	sockets, err1 := meter.Int64ObservableGauge("modulith.ws.sockets",
		metric.WithDescription("Open WebSockets."), metric.WithUnit("{socket}"))
	projects, err2 := meter.Int64ObservableGauge("modulith.ws.projects",
		metric.WithDescription("Projects at least one WebSocket has joined."), metric.WithUnit("{project}"))
	if err := errors.Join(err1, err2); err != nil {
		return func() {}
	}
	reg, err := meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		h.mu.RLock()
		defer h.mu.RUnlock()
		o.ObserveInt64(sockets, int64(len(h.sockets)))
		o.ObserveInt64(projects, int64(len(h.projects)))
		return nil
	}, sockets, projects)
	if err != nil {
		return func() {}
	}
	return func() { reg.Unregister() }
}
