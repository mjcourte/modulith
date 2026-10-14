package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"gyoza/internal/engine"
	gnats "gyoza/platform/nats"
	"gyoza/platform/telemetry"
	"gyoza/services/gateway/internal/rest"
	"gyoza/services/storage"
)

// The WebSocket's own errors, next to REST's codes (rest.Code).
var (
	// ErrNotJoined: edits and user state need the project joined first
	// (which checked read access and sent the document).
	ErrNotJoined = errors.New("ws: project not joined")
	// ErrWrongReplica: an edit must be stamped by this socket's engine,
	// with the replica ID from hello. Another replica's ID would let a
	// client win the ties meant for that replica; an unstamped edit
	// couldn't be applied the same everywhere.
	ErrWrongReplica = errors.New("ws: edit not stamped with this socket's replica")
)

var wsErrors = []struct {
	err  error
	code string
}{
	{ErrNotJoined, "NOT_JOINED"},
	{ErrWrongReplica, "WRONG_REPLICA"},
}

// socket is one browser tab's connection. readLoop handles the client's
// messages one at a time (in order: a join completes before a later edit);
// writeLoop alone writes to conn, draining out.
type socket struct {
	hub     *Hub
	conn    *websocket.Conn
	actor   string // the session's user
	replica string
	ctx     context.Context // ends when the socket closes
	kill    context.CancelCauseFunc
	out     chan []byte

	mu sync.Mutex
	// joined holds the joined projects. A nil value is live; a non-nil one
	// is joining, its traffic buffered until the document is sent.
	joined map[string]*pending
}

type pending struct{ msgs []pendingMsg }

type pendingMsg struct {
	seq uint64
	b   []byte
}

// closeCause is how close asks writeLoop to close the connection.
type closeCause struct {
	code   websocket.StatusCode
	reason string
}

func (c closeCause) Error() string { return fmt.Sprintf("ws: close %d %s", c.code, c.reason) }

// close ends the socket with a close code. Safe from any goroutine; the
// first cause wins.
func (s *socket) close(code websocket.StatusCode, reason string) {
	s.kill(closeCause{code, reason})
}

// push queues encoded bytes, never blocking: a full queue closes the socket.
// Called with s.mu held, or before other goroutines can push.
func (s *socket) push(b []byte) {
	select {
	case s.out <- b:
	default:
		s.close(CloseTooSlow, "too slow")
	}
}

func (s *socket) send(msg any) {
	b, err := json.Marshal(msg)
	if err != nil {
		slog.Error("ws: encode message", "err", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.push(b)
}

// deliver queues fanned-out traffic, or buffers it while the project is
// joining.
func (s *socket) deliver(projectID string, seq uint64, b []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.joined[projectID]
	switch {
	case !ok: // left meanwhile
	case p == nil:
		s.push(b)
	case len(p.msgs) >= sendQueue:
		s.close(CloseTooSlow, "too slow")
	default:
		p.msgs = append(p.msgs, pendingMsg{seq, b})
	}
}

func (s *socket) writeLoop() {
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()
	for {
		select {
		case b := <-s.out:
			ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
			err := s.conn.Write(ctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				s.kill(err)
			}
		case <-ping.C:
			// Finds a client that vanished without closing. The pong is
			// read by readLoop's Read.
			ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
			err := s.conn.Ping(ctx)
			cancel()
			if err != nil {
				s.kill(err)
			}
		case <-s.ctx.Done():
			code, reason := websocket.StatusNormalClosure, ""
			var c closeCause
			if errors.As(context.Cause(s.ctx), &c) {
				code, reason = c.code, c.reason
			}
			s.conn.Close(code, reason)
			return
		}
	}
}

// readLoop returns when the connection fails or closes (writeLoop's Close
// makes Read return too).
func (s *socket) readLoop() {
	for {
		typ, data, err := s.conn.Read(context.Background())
		if err != nil {
			return
		}
		var in inMsg
		if typ != websocket.MessageText {
			s.fail(s.ctx, in, rest.BadRequest("messages must be JSON text frames"))
			continue
		}
		if err := json.Unmarshal(data, &in); err != nil {
			s.fail(s.ctx, in, rest.BadRequest("message is not JSON: %v", err))
			continue
		}
		s.handle(in)
	}
}

func (s *socket) handle(in inMsg) {
	countMessage(s.ctx, in.Type)
	ctx, span := s.startSpan(in)
	defer span.End()
	var err error
	switch in.Type {
	case "join":
		err = s.join(ctx, in.ProjectID)
	case "leave":
		s.leave(in.ProjectID)
	case "edit":
		err = s.edit(ctx, in)
	case "userstate":
		err = s.userState(in)
	default:
		err = rest.BadRequest("unknown message type %q", in.Type)
	}
	if err != nil {
		code, failed := s.fail(ctx, in, err)
		endSpan(span, err, code, failed)
	}
}

var tracer = otel.Tracer("gyoza/services/gateway/internal/ws")

// startSpan starts a message's span, the root of a new trace (the socket
// isn't part of one). Joins are always traced; edits are a hot path
// (telemetry.Hot). Presence and leaves get none: presence is the busiest
// message and its traces would say nothing; a leave does no I/O.
func (s *socket) startSpan(in inMsg) (context.Context, trace.Span) {
	opts := []trace.SpanStartOption{trace.WithNewRoot(), trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(attribute.String("enduser.id", s.actor), attribute.String("gyoza.project_id", in.ProjectID))}
	switch in.Type {
	case "join":
	case "edit":
		opts = append(opts, telemetry.Hot)
	default:
		return s.ctx, trace.SpanFromContext(s.ctx) // a no-op span
	}
	return tracer.Start(s.ctx, "ws."+in.Type, opts...)
}

// endSpan records a message's error: a failure (an internal error) marks
// the span failed; a refusal (forbidden, not joined, ...) is an expected
// outcome, kept as an attribute so refusals don't look like an outage.
func endSpan(span trace.Span, err error, code string, failed bool) {
	if failed {
		span.RecordError(err)
		span.SetStatus(codes.Error, code)
		return
	}
	span.SetAttributes(attribute.String("gyoza.refused", code))
}

// fail reports err to the client with the same codes as REST, and returns
// the code and whether it was a failure rather than a refusal. Unmapped
// errors are logged, not shown: their text may hold internals.
func (s *socket) fail(ctx context.Context, in inMsg, err error) (code string, failed bool) {
	msg := errorMsg{Type: "error", ID: in.ID, ProjectID: in.ProjectID, Message: err.Error()}
	for _, e := range wsErrors {
		if errors.Is(err, e.err) {
			msg.Code = e.code
		}
	}
	if msg.Code == "" {
		var status int
		status, msg.Code = rest.Code(err)
		if status == http.StatusInternalServerError {
			slog.ErrorContext(ctx, "ws: message failed", "type", in.Type, "project", in.ProjectID, "err", err)
			msg.Message = "internal error"
			failed = true
		}
	}
	s.send(msg)
	return msg.Code, failed
}

func (s *socket) isJoined(projectID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.joined[projectID]
	return ok
}

// join registers for the project's traffic before reading the document, so
// nothing published in between is missed: it's buffered, then sent after
// the document unless the document already covers it. GetDocument checks
// read access; until it has, the buffer is never sent. Joining again resends
// the document (a resync).
func (s *socket) join(ctx context.Context, projectID string) error {
	if projectID == "" {
		return rest.BadRequest("projectId is required")
	}
	s.mu.Lock()
	s.joined[projectID] = &pending{}
	s.mu.Unlock()
	s.hub.add(projectID, s)

	doc, err := s.hub.docs.GetDocument(ctx, s.actor, projectID)
	if err != nil {
		s.leave(projectID)
		return err
	}
	b, err := json.Marshal(documentMsg{Type: "document", ProjectID: projectID, Format: string(doc.Snapshot.Format),
		Version: doc.Snapshot.Version, Seq: doc.Seq, Payload: doc.Snapshot.Payload})
	if err != nil {
		s.leave(projectID)
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.joined[projectID]
	if !ok || p == nil {
		return nil // closed meanwhile (drop cleared it)
	}
	s.push(b)
	for _, m := range p.msgs {
		if m.seq == 0 || m.seq > doc.Seq {
			s.push(m.b)
		}
	}
	s.joined[projectID] = nil
	return nil
}

func (s *socket) leave(projectID string) {
	s.mu.Lock()
	delete(s.joined, projectID)
	s.mu.Unlock()
	s.hub.remove(projectID, s)
}

// recheckLoop re-checks read access to the joined projects every interval
// until the socket closes. Joining checked it once; without this, a user
// unshared or removed from the org would keep receiving the project's
// traffic until their session expired.
func (s *socket) recheckLoop(interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			s.recheck()
		}
	}
}

// recheck leaves every live project the actor may no longer read, telling
// the client with a FORBIDDEN error naming it. Projects still joining are
// skipped: their GetDocument is checking. Any other failure (iam
// unreachable) keeps the project: the next check retries.
func (s *socket) recheck() {
	// Once a minute on every socket: a hot path.
	ctx, span := tracer.Start(s.ctx, "ws.recheck", trace.WithNewRoot(), telemetry.Hot,
		trace.WithAttributes(attribute.String("enduser.id", s.actor)))
	defer span.End()
	s.mu.Lock()
	var live []string
	for p, pend := range s.joined {
		if pend == nil {
			live = append(live, p)
		}
	}
	s.mu.Unlock()
	for _, p := range live {
		err := s.hub.authz.CanRead(ctx, s.actor, storage.Owner{Kind: storage.OwnerProject, ID: p}, p)
		switch {
		case err == nil:
		case errors.Is(err, storage.ErrForbidden):
			s.leave(p)
			code, _ := s.fail(ctx, inMsg{ProjectID: p}, err)
			span.AddEvent("left", trace.WithAttributes(attribute.String("gyoza.project_id", p), attribute.String("gyoza.refused", code)))
		case s.ctx.Err() == nil:
			slog.WarnContext(ctx, "ws: access re-check failed", "project", p, "err", err)
		}
	}
}

// edit publishes a browser's delta for documents (and every other client)
// to apply. The gateway never sets the delta's origin: documents bounds the
// stamps of deltas without one (engine/PLAN.md row 2).
func (s *socket) edit(ctx context.Context, in inMsg) error {
	if !s.isJoined(in.ProjectID) {
		return ErrNotJoined
	}
	if in.ID == "" || len(in.ID) > maxEditID {
		return rest.BadRequest("id is required, at most %d characters", maxEditID)
	}
	project := storage.Owner{Kind: storage.OwnerProject, ID: in.ProjectID}
	if err := s.hub.authz.CanWrite(ctx, s.actor, project, in.ProjectID); err != nil {
		return err
	}
	lamport, replica, err := engine.StampOf(engine.DeltaInput{Format: engine.WireFormat(in.Format), Version: in.Version, Payload: in.Payload})
	if err != nil {
		return err
	}
	if lamport == 0 || replica != s.replica {
		return ErrWrongReplica
	}
	seq, err := s.hub.bus.PublishDelta(ctx, gnats.Delta{ProjectID: in.ProjectID, ID: in.ID, ActorID: s.actor,
		Format: in.Format, Version: in.Version, Payload: in.Payload})
	if err != nil {
		return err
	}
	s.send(ackMsg{Type: "ack", ID: in.ID, Seq: seq})
	return nil
}

// userState publishes presence as the session's user, never one the client
// names.
func (s *socket) userState(in inMsg) error {
	if !s.isJoined(in.ProjectID) {
		return ErrNotJoined
	}
	if in.Kind != gnats.UserStateKindViewing && in.Kind != gnats.UserStateKindCursor {
		return rest.BadRequest("kind must be %s or %s", gnats.UserStateKindViewing, gnats.UserStateKindCursor)
	}
	if len(in.Data) > maxUserState {
		return rest.BadRequest("data is over %d bytes", maxUserState)
	}
	data := in.Data
	if len(data) == 0 {
		data = json.RawMessage("null")
	}
	return s.hub.bus.PublishUserState(s.ctx, UserState{ProjectID: in.ProjectID, UserID: s.actor, Kind: in.Kind, Data: data})
}
