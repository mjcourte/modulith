package ws

import (
	"encoding/json"
	"time"
)

// The protocol: JSON text frames, each an object with a "type". Delta and
// snapshot bytes are base64 (encoding/json's encoding of []byte). See
// PLAN.md, step 3.

// inMsg is any client message; which fields count depends on Type.
type inMsg struct {
	Type      string `json:"type"` // join, leave, edit, userstate
	ProjectID string `json:"projectId"`
	// edit
	ID      string `json:"id"` // the client's ID for the edit (Nats-Msg-Id), echoed in ack/error
	Format  string `json:"format"`
	Version int    `json:"version"`
	Payload []byte `json:"payload"`
	// userstate
	Kind string          `json:"kind"` // viewing, cursorpos
	Data json.RawMessage `json:"data"`
}

// helloMsg is the first message on every socket. The client's engine stamps
// its edits with Replica, its clock offset by ServerTime.
type helloMsg struct {
	Type       string `json:"type"` // hello
	Replica    string `json:"replica"`
	ServerTime int64  `json:"serverTime"` // milliseconds since the epoch
}

// documentMsg answers a join: the document's state, covering the stream up
// to Seq. Delta and snapshot messages for the project follow, all after Seq.
type documentMsg struct {
	Type      string `json:"type"` // document
	ProjectID string `json:"projectId"`
	Format    string `json:"format"`
	Version   int    `json:"version"`
	Seq       uint64 `json:"seq"`
	Payload   []byte `json:"payload"`
}

type deltaMsg struct {
	Type      string `json:"type"` // delta
	ProjectID string `json:"projectId"`
	ID        string `json:"id"`
	Seq       uint64 `json:"seq"`
	ActorID   string `json:"actorId"`
	Format    string `json:"format"`
	Version   int    `json:"version"`
	Payload   []byte `json:"payload"`
}

// snapshotMsg is a snapshot pointer: the client fetches the snapshot with
// GET /api/projects/{projectId}/snapshots/{objectId} and merges it.
type snapshotMsg struct {
	Type      string    `json:"type"` // snapshot
	ProjectID string    `json:"projectId"`
	Version   int       `json:"version"`
	Seq       uint64    `json:"seq"`
	ObjectID  string    `json:"objectId"`
	EditedBy  string    `json:"editedBy,omitempty"`
	EditedAt  time.Time `json:"editedAt,omitzero"`
}

// userStateMsg comes from every user in the project, the receiver included
// (the client filters its own).
type userStateMsg struct {
	Type      string          `json:"type"` // userstate
	ProjectID string          `json:"projectId"`
	UserID    string          `json:"userId"`
	Kind      string          `json:"kind"`
	Data      json.RawMessage `json:"data"`
}

// ackMsg: the edit is in the stream at Seq.
type ackMsg struct {
	Type string `json:"type"` // ack
	ID   string `json:"id"`
	Seq  uint64 `json:"seq"`
}

// errorMsg uses REST's error codes (rest.Code) plus the WebSocket's own.
// ID names the refused edit; ProjectID the refused join or message.
type errorMsg struct {
	Type      string `json:"type"` // error
	ID        string `json:"id,omitempty"`
	ProjectID string `json:"projectId,omitempty"`
	Code      string `json:"code"`
	Message   string `json:"message"`
}
