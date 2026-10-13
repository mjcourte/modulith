package wal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"gyoza/internal/engine"
	gnats "gyoza/platform/nats"
)

// Headers on delta messages. Format and version describe the payload; the
// actor is who made the edit (the gateway sets it after checking write
// access, as it does for actor_id on gRPC); the origin is the documents
// replica that published it, if any.
const (
	headerFormat  = "Gyoza-Format"
	headerVersion = "Gyoza-Version"
	headerActor   = "Gyoza-Actor"
	headerOrigin  = "Gyoza-Origin"
)

// ErrBadDelta marks a delta that can never be applied (malformed, wrong
// format). Consume terminates such a message instead of redelivering it.
var ErrBadDelta = errors.New("wal: delta can't be applied")

// Delta is one delta message.
type Delta struct {
	ProjectID string
	// ID is the message ID (Nats-Msg-Id): a UUIDv7, so IDs sort by time.
	// PublishDelta makes one if it's empty; set it to retry a publish whose
	// outcome is unknown, and the stream stores it once.
	ID      string
	Seq     uint64 // stream sequence; set on messages read from the stream
	ActorID string
	Origin  string // the documents replica that published it, or ""
	Delta   engine.DeltaInput
}

// Pointer announces a stored snapshot to clients (DOCUMENTS stream,
// document.{projectID}.snapshot). JSON, a few hundred bytes.
type Pointer struct {
	Version  int    `json:"version"`
	Seq      uint64 `json:"seq"` // last delta sequence the snapshot covers
	ObjectID string `json:"objectId"`
	// The last change the snapshot holds (a `touch`): who and when.
	EditedBy string    `json:"editedBy,omitempty"`
	EditedAt time.Time `json:"editedAt,omitzero"`
}

// Log publishes to and reads from the DOCUMENTS stream.
type Log struct {
	js jetstream.JetStream
}

func New(js jetstream.JetStream) *Log { return &Log{js: js} }

// PublishDelta publishes an edit and returns its stream sequence. Publishes
// with the same ID within the stream's dedupe window (2m) are stored once and
// return the first one's sequence. Deduplication only saves work: applying a
// delta twice is harmless.
func (l *Log) PublishDelta(ctx context.Context, d Delta) (uint64, error) {
	subj, err := gnats.DocumentDelta(d.ProjectID)
	if err != nil {
		return 0, err
	}
	if d.ID == "" {
		d.ID = uuid.Must(uuid.NewV7()).String()
	}
	msg := &natsgo.Msg{Subject: subj, Data: d.Delta.Payload, Header: natsgo.Header{}}
	msg.Header.Set(jetstream.MsgIDHeader, d.ID)
	msg.Header.Set(headerFormat, string(d.Delta.Format))
	msg.Header.Set(headerVersion, strconv.Itoa(d.Delta.Version))
	if d.ActorID != "" {
		msg.Header.Set(headerActor, d.ActorID)
	}
	if d.Origin != "" {
		msg.Header.Set(headerOrigin, d.Origin)
	}
	ack, err := l.js.PublishMsg(ctx, msg)
	if err != nil {
		return 0, fmt.Errorf("wal: publish delta of %s: %w", d.ProjectID, err)
	}
	return ack.Sequence, nil
}

// PublishSnapshot announces a stored snapshot. Call Compact after it, never
// before: compacting first loses deltas if this publish fails.
func (l *Log) PublishSnapshot(ctx context.Context, projectID string, p Pointer) error {
	subj, err := gnats.DocumentSnapshot(projectID)
	if err != nil {
		return err
	}
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if _, err := l.js.Publish(ctx, subj, data); err != nil {
		return fmt.Errorf("wal: publish snapshot pointer of %s: %w", projectID, err)
	}
	return nil
}

// Compact removes the deltas a stored snapshot covers and older pointers.
func (l *Log) Compact(ctx context.Context, projectID string, coveredSeq uint64) error {
	return gnats.Compact(ctx, l.js, projectID, coveredSeq)
}

// DeltasAfter calls fn for each of a project's deltas after seq, oldest
// first, without a consumer (Direct Get). A message that can't be decoded is
// skipped and reported to onBad: one malformed message must not make a
// project impossible to load.
func (l *Log) DeltasAfter(ctx context.Context, projectID string, seq uint64, fn func(Delta) error, onBad func(error)) error {
	return gnats.DeltasAfter(ctx, l.js, projectID, seq, func(m *jetstream.RawStreamMsg) error {
		d, err := decode(m.Subject, m.Sequence, m.Header, m.Data)
		if err != nil {
			onBad(err)
			return nil
		}
		return fn(d)
	})
}

// ConsumerConfig names the durable consumer. DeliverNew starts a newly
// created consumer at new messages instead of the stream's start; tests use
// it so they don't replay other tests' deltas.
type ConsumerConfig struct {
	Durable    string
	DeliverNew bool
}

// Consume runs the durable consumer over every project's deltas until ctx
// ends. A message is acked when fn returns nil and redelivered after a delay
// when it fails. One that can't be decoded, or that fn rejects with
// ErrBadDelta, is terminated: redelivery can't fix it. onBad hears of every
// failure, for logging.
func (l *Log) Consume(ctx context.Context, cfg ConsumerConfig, fn func(context.Context, Delta) error, onBad func(error)) error {
	policy := jetstream.DeliverAllPolicy
	if cfg.DeliverNew {
		policy = jetstream.DeliverNewPolicy
	}
	cons, err := l.js.CreateOrUpdateConsumer(ctx, gnats.Documents.Name, jetstream.ConsumerConfig{
		Durable:       cfg.Durable,
		FilterSubject: gnats.PatternDocumentDelta,
		AckPolicy:     jetstream.AckExplicitPolicy,
		DeliverPolicy: policy,
		AckWait:       30 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("wal: create consumer %s: %w", cfg.Durable, err)
	}
	cc, err := cons.Consume(func(m jetstream.Msg) {
		meta, err := m.Metadata()
		if err != nil {
			onBad(err)
			m.Term()
			return
		}
		d, err := decode(m.Subject(), meta.Sequence.Stream, m.Headers(), m.Data())
		if err != nil {
			onBad(err)
			m.Term()
			return
		}
		if err := fn(ctx, d); errors.Is(err, ErrBadDelta) {
			onBad(fmt.Errorf("wal: dropping delta %d of %s: %w", d.Seq, d.ProjectID, err))
			m.Term()
			return
		} else if err != nil {
			onBad(fmt.Errorf("wal: apply delta %d of %s: %w", d.Seq, d.ProjectID, err))
			m.NakWithDelay(5 * time.Second)
			return
		}
		m.Ack()
	})
	if err != nil {
		return fmt.Errorf("wal: consume: %w", err)
	}
	<-ctx.Done()
	cc.Drain() // finish the message in hand
	<-cc.Closed()
	return nil
}

func decode(subject string, seq uint64, h natsgo.Header, data []byte) (Delta, error) {
	// document.{projectID}.delta
	parts := strings.Split(subject, ".")
	if len(parts) != 3 || parts[0] != "document" || parts[2] != "delta" {
		return Delta{}, fmt.Errorf("wal: not a delta subject: %q", subject)
	}
	version, err := strconv.Atoi(h.Get(headerVersion))
	if err != nil {
		return Delta{}, fmt.Errorf("wal: delta %d on %s: bad %s header: %w", seq, subject, headerVersion, err)
	}
	format := engine.WireFormat(h.Get(headerFormat))
	if format == "" {
		return Delta{}, fmt.Errorf("wal: delta %d on %s: no format header", seq, subject)
	}
	return Delta{
		ProjectID: parts[1],
		ID:        h.Get(jetstream.MsgIDHeader),
		Seq:       seq,
		ActorID:   h.Get(headerActor),
		Origin:    h.Get(headerOrigin),
		Delta:     engine.DeltaInput{Format: format, Version: version, Payload: data},
	}, nil
}
