package wal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/mjcourte/modulith/internal/engine"
	gnats "github.com/mjcourte/modulith/platform/nats"
	"github.com/mjcourte/modulith/platform/telemetry"
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

// Pointer announces a stored snapshot to clients. The delta and pointer
// message formats are in platform/nats, shared with the gateway.
type Pointer = gnats.Pointer

var tracer = otel.Tracer("modulith/services/documents/internal/wal")

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
	if d.ID == "" {
		d.ID = uuid.Must(uuid.NewV7()).String()
	}
	msg, err := gnats.Delta{ProjectID: d.ProjectID, ID: d.ID, ActorID: d.ActorID, Origin: d.Origin,
		Format: string(d.Delta.Format), Version: d.Delta.Version, Payload: d.Delta.Payload}.Msg(ctx)
	if err != nil {
		return 0, err
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
	msg := &natsgo.Msg{Subject: subj, Data: data, Header: natsgo.Header{}}
	gnats.InjectTrace(ctx, msg.Header)
	if _, err := l.js.PublishMsg(ctx, msg); err != nil {
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
	stopMetrics := consumerMetrics(cons)
	defer stopMetrics()
	cc, err := cons.Consume(func(m jetstream.Msg) {
		meta, err := m.Metadata()
		if err != nil {
			onBad(err)
			m.Term()
			deltas.Add(ctx, 1, outcome("dropped"))
			return
		}
		d, err := decode(m.Subject(), meta.Sequence.Stream, m.Headers(), m.Data())
		if err != nil {
			onBad(err)
			m.Term()
			deltas.Add(ctx, 1, outcome("dropped"))
			return
		}
		// The publisher's trace continues here: a browser's edit is one trace
		// from the gateway's ws.edit on. A delta without one (documents' own)
		// starts a trace of its own, a hot path like the edits.
		mctx, span := tracer.Start(gnats.TraceContext(ctx, m.Headers()), "document.apply",
			trace.WithSpanKind(trace.SpanKindConsumer), telemetry.Hot,
			trace.WithAttributes(attribute.String("modulith.project_id", d.ProjectID), attribute.Int64("modulith.seq", int64(d.Seq))))
		defer span.End()
		if err := fn(mctx, d); errors.Is(err, ErrBadDelta) {
			span.SetAttributes(attribute.String("modulith.refused", "BAD_DELTA"))
			onBad(fmt.Errorf("wal: dropping delta %d of %s: %w", d.Seq, d.ProjectID, err))
			m.Term()
			deltas.Add(ctx, 1, outcome("dropped"))
			return
		} else if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "apply failed")
			onBad(fmt.Errorf("wal: apply delta %d of %s: %w", d.Seq, d.ProjectID, err))
			m.NakWithDelay(5 * time.Second)
			deltas.Add(ctx, 1, outcome("retried"))
			return
		}
		m.Ack()
		deltas.Add(ctx, 1, outcome("applied"))
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
	m, err := gnats.DecodeDelta(subject, seq, h, data)
	if err != nil {
		return Delta{}, fmt.Errorf("wal: %w", err)
	}
	return Delta{
		ProjectID: m.ProjectID,
		ID:        m.ID,
		Seq:       m.Seq,
		ActorID:   m.ActorID,
		Origin:    m.Origin,
		Delta:     engine.DeltaInput{Format: engine.WireFormat(m.Format), Version: m.Version, Payload: m.Payload},
	}, nil
}

var (
	meter = otel.Meter("modulith/services/documents/internal/wal")
	// deltas counts the consumer's deltas by outcome: applied, dropped
	// (unreadable or rejected, never retried) or retried.
	deltas, _ = meter.Int64Counter("modulith.documents.deltas",
		metric.WithDescription("Deltas the documents consumer handled, by outcome."), metric.WithUnit("{delta}"))
)

func outcome(o string) metric.AddOption {
	return metric.WithAttributes(attribute.String("outcome", o))
}

// consumerMetrics reports how many deltas wait for cons (its lag behind the
// stream) at each collection, until the returned function stops it. The
// count is a JetStream API call, bounded so a slow server can't stall
// collection.
func consumerMetrics(cons jetstream.Consumer) func() {
	pending, err := meter.Int64ObservableGauge("modulith.documents.consumer.pending",
		metric.WithDescription("Deltas in the stream the documents consumer hasn't handled yet."), metric.WithUnit("{delta}"))
	if err != nil {
		return func() {}
	}
	reg, err := meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		info, err := cons.Info(ctx)
		if err != nil {
			return err
		}
		o.ObserveInt64(pending, int64(info.NumPending))
		return nil
	}, pending)
	if err != nil {
		return func() {}
	}
	return func() { reg.Unregister() }
}
