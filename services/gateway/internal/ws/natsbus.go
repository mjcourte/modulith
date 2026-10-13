package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	gnats "gyoza/platform/nats"
)

// NATSBus is the Bus on NATS: one ordered consumer on the DOCUMENTS stream
// and one core subscription on userstate.> per gateway instance, whatever
// the number of sockets (platform/nats README).
type NATSBus struct {
	conn *natsgo.Conn
	js   jetstream.JetStream
}

func NewNATSBus(c *gnats.Client) *NATSBus { return &NATSBus{conn: c.Conn, js: c.JS} }

func (b *NATSBus) PublishDelta(ctx context.Context, d gnats.Delta) (uint64, error) {
	msg, err := d.Msg()
	if err != nil {
		return 0, err
	}
	ack, err := b.js.PublishMsg(ctx, msg)
	if err != nil {
		return 0, fmt.Errorf("ws: publish delta of %s: %w", d.ProjectID, err)
	}
	return ack.Sequence, nil
}

func (b *NATSBus) PublishUserState(ctx context.Context, u UserState) error {
	var subj string
	var err error
	switch u.Kind {
	case gnats.UserStateKindViewing:
		subj, err = gnats.UserStateViewing(u.ProjectID, u.UserID)
	case gnats.UserStateKindCursor:
		subj, err = gnats.UserStateCursor(u.ProjectID, u.UserID)
	default:
		err = fmt.Errorf("ws: unknown user state kind %q", u.Kind)
	}
	if err != nil {
		return err
	}
	return b.conn.Publish(subj, u.Data)
}

// Run starts at new messages: a joining socket gets everything older in its
// document. The ordered consumer recreates itself after a gap (a NATS
// restart), resuming after the last message it delivered.
func (b *NATSBus) Run(ctx context.Context, f Feed) error {
	cons, err := b.js.OrderedConsumer(ctx, gnats.Documents.Name, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{gnats.PatternDocumentDelta, gnats.PatternDocumentSnapshot},
		DeliverPolicy:  jetstream.DeliverNewPolicy,
	})
	if err != nil {
		return fmt.Errorf("ws: create consumer: %w", err)
	}
	cc, err := cons.Consume(func(m jetstream.Msg) {
		meta, err := m.Metadata()
		if err != nil {
			slog.Warn("ws: message without metadata", "subject", m.Subject(), "err", err)
			return
		}
		if projectID, ok := gnats.SnapshotProject(m.Subject()); ok {
			var p gnats.Pointer
			if err := json.Unmarshal(m.Data(), &p); err != nil {
				slog.Warn("ws: bad snapshot pointer", "subject", m.Subject(), "err", err)
				return
			}
			f.Snapshot(projectID, p)
			return
		}
		d, err := gnats.DecodeDelta(m.Subject(), meta.Sequence.Stream, m.Headers(), m.Data())
		if err != nil {
			slog.Warn("ws: bad delta", "subject", m.Subject(), "err", err)
			return
		}
		f.Delta(d)
	})
	if err != nil {
		return fmt.Errorf("ws: consume: %w", err)
	}
	defer cc.Stop()

	sub, err := b.conn.Subscribe("userstate.>", func(m *natsgo.Msg) {
		projectID, userID, kind, ok := gnats.ParseUserState(m.Subject)
		if !ok || !json.Valid(m.Data) {
			return
		}
		f.UserState(UserState{ProjectID: projectID, UserID: userID, Kind: kind, Data: m.Data})
	})
	if err != nil {
		return fmt.Errorf("ws: subscribe to user state: %w", err)
	}
	defer sub.Unsubscribe()

	<-ctx.Done()
	return nil
}
