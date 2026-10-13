package nats

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// The DOCUMENTS stream's messages, as every publisher and reader sees them
// (documents and the gateway). Payloads stay opaque bytes here: decoding
// them is the engine's job.

// Headers on delta messages. Format and version describe the payload; the
// actor is who made the edit; the origin is the documents replica that
// published it. Readers trust the stamps of deltas with an origin, so only
// documents may set it: the gateway, publishing browsers' deltas, never does.
const (
	HeaderFormat  = "Gyoza-Format"
	HeaderVersion = "Gyoza-Version"
	HeaderActor   = "Gyoza-Actor"
	HeaderOrigin  = "Gyoza-Origin"
)

// Delta is one delta message.
type Delta struct {
	ProjectID string
	// ID is the message ID (Nats-Msg-Id): the stream stores a repeated ID
	// once within its dedupe window, so a publish can be retried.
	ID      string
	Seq     uint64 // stream sequence; set on messages read from the stream
	ActorID string
	Origin  string
	Format  string
	Version int
	Payload []byte
}

// Msg builds the message to publish.
func (d Delta) Msg() (*natsgo.Msg, error) {
	subj, err := DocumentDelta(d.ProjectID)
	if err != nil {
		return nil, err
	}
	msg := &natsgo.Msg{Subject: subj, Data: d.Payload, Header: natsgo.Header{}}
	if d.ID != "" {
		msg.Header.Set(jetstream.MsgIDHeader, d.ID)
	}
	msg.Header.Set(HeaderFormat, d.Format)
	msg.Header.Set(HeaderVersion, strconv.Itoa(d.Version))
	if d.ActorID != "" {
		msg.Header.Set(HeaderActor, d.ActorID)
	}
	if d.Origin != "" {
		msg.Header.Set(HeaderOrigin, d.Origin)
	}
	return msg, nil
}

// DecodeDelta reads a delta message received on subject at stream sequence
// seq.
func DecodeDelta(subject string, seq uint64, h natsgo.Header, data []byte) (Delta, error) {
	projectID, ok := projectOf(subject, "delta")
	if !ok {
		return Delta{}, fmt.Errorf("nats: not a delta subject: %q", subject)
	}
	version, err := strconv.Atoi(h.Get(HeaderVersion))
	if err != nil {
		return Delta{}, fmt.Errorf("nats: delta %d on %s: bad %s header: %w", seq, subject, HeaderVersion, err)
	}
	format := h.Get(HeaderFormat)
	if format == "" {
		return Delta{}, fmt.Errorf("nats: delta %d on %s: no format header", seq, subject)
	}
	return Delta{
		ProjectID: projectID,
		ID:        h.Get(jetstream.MsgIDHeader),
		Seq:       seq,
		ActorID:   h.Get(HeaderActor),
		Origin:    h.Get(HeaderOrigin),
		Format:    format,
		Version:   version,
		Payload:   data,
	}, nil
}

// Pointer announces a stored snapshot (document.{projectID}.snapshot).
// JSON, a few hundred bytes; documents is the only publisher.
type Pointer struct {
	Version  int    `json:"version"`
	Seq      uint64 `json:"seq"` // last delta sequence the snapshot covers
	ObjectID string `json:"objectId"`
	// The last change the snapshot holds (a `touch`): who and when.
	EditedBy string    `json:"editedBy,omitempty"`
	EditedAt time.Time `json:"editedAt,omitzero"`
}

// SnapshotProject returns the project of a snapshot pointer subject.
func SnapshotProject(subject string) (string, bool) { return projectOf(subject, "snapshot") }

// projectOf reads document.{projectID}.{kind}.
func projectOf(subject, kind string) (string, bool) {
	parts := strings.Split(subject, ".")
	if len(parts) != 3 || parts[0] != "document" || parts[2] != kind || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

// UserStateKinds are the user state subjects' last tokens.
const (
	UserStateKindViewing = "viewing"
	UserStateKindCursor  = "cursorpos"
)

// ParseUserState reads userstate.{projectID}.{userID}.{kind}.
func ParseUserState(subject string) (projectID, userID, kind string, ok bool) {
	parts := strings.Split(subject, ".")
	if len(parts) != 4 || parts[0] != "userstate" || parts[1] == "" || parts[2] == "" {
		return "", "", "", false
	}
	switch parts[3] {
	case UserStateKindViewing, UserStateKindCursor:
		return parts[1], parts[2], parts[3], true
	}
	return "", "", "", false
}
