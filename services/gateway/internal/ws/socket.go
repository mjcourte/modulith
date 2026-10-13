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

	"gyoza/internal/engine"
	gnats "gyoza/platform/nats"
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
			s.fail(in, rest.BadRequest("messages must be JSON text frames"))
			continue
		}
		if err := json.Unmarshal(data, &in); err != nil {
			s.fail(in, rest.BadRequest("message is not JSON: %v", err))
			continue
		}
		s.handle(in)
	}
}

func (s *socket) handle(in inMsg) {
	var err error
	switch in.Type {
	case "join":
		err = s.join(in.ProjectID)
	case "leave":
		s.leave(in.ProjectID)
	case "edit":
		err = s.edit(in)
	case "userstate":
		err = s.userState(in)
	default:
		err = rest.BadRequest("unknown message type %q", in.Type)
	}
	if err != nil {
		s.fail(in, err)
	}
}

// fail reports err to the client with the same codes as REST. Unmapped
// errors are logged, not shown: their text may hold internals.
func (s *socket) fail(in inMsg, err error) {
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
			slog.Error("ws: message failed", "type", in.Type, "project", in.ProjectID, "err", err)
			msg.Message = "internal error"
		}
	}
	s.send(msg)
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
func (s *socket) join(projectID string) error {
	if projectID == "" {
		return rest.BadRequest("projectId is required")
	}
	s.mu.Lock()
	s.joined[projectID] = &pending{}
	s.mu.Unlock()
	s.hub.add(projectID, s)

	doc, err := s.hub.docs.GetDocument(s.ctx, s.actor, projectID)
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

// edit publishes a browser's delta for documents (and every other client)
// to apply. The gateway never sets the delta's origin: documents bounds the
// stamps of deltas without one (engine/PLAN.md row 2).
func (s *socket) edit(in inMsg) error {
	if !s.isJoined(in.ProjectID) {
		return ErrNotJoined
	}
	if in.ID == "" || len(in.ID) > maxEditID {
		return rest.BadRequest("id is required, at most %d characters", maxEditID)
	}
	project := storage.Owner{Kind: storage.OwnerProject, ID: in.ProjectID}
	if err := s.hub.authz.CanWrite(s.ctx, s.actor, project, in.ProjectID); err != nil {
		return err
	}
	lamport, replica, err := engine.StampOf(engine.DeltaInput{Format: engine.WireFormat(in.Format), Version: in.Version, Payload: in.Payload})
	if err != nil {
		return err
	}
	if lamport == 0 || replica != s.replica {
		return ErrWrongReplica
	}
	seq, err := s.hub.bus.PublishDelta(s.ctx, gnats.Delta{ProjectID: in.ProjectID, ID: in.ID, ActorID: s.actor,
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
