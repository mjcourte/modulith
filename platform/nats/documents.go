package nats

import (
	"context"
	"errors"
	"fmt"

	"github.com/nats-io/nats.go/jetstream"
)

// Consumer-free reads of one project's messages, by Direct Get. Use these for
// per-project lookups (loading a document) instead of creating a consumer.

// ErrNoSnapshot means a project has no snapshot pointer in the stream: it
// never had one, or MaxAge expired it.
var ErrNoSnapshot = errors.New("nats: no snapshot pointer")

// LatestSnapshot returns a project's latest snapshot pointer message.
func LatestSnapshot(ctx context.Context, js jetstream.JetStream, projectID string) (*jetstream.RawStreamMsg, error) {
	subj, err := DocumentSnapshot(projectID)
	if err != nil {
		return nil, err
	}
	stream, err := js.Stream(ctx, Documents.Name)
	if err != nil {
		return nil, fmt.Errorf("latest snapshot: %w", err)
	}
	msg, err := stream.GetLastMsgForSubject(ctx, subj)
	if errors.Is(err, jetstream.ErrMsgNotFound) {
		return nil, fmt.Errorf("%w for %s", ErrNoSnapshot, projectID)
	}
	if err != nil {
		return nil, fmt.Errorf("latest snapshot of %s: %w", projectID, err)
	}
	return msg, nil
}

// DeltasAfter calls fn for each of a project's deltas with a sequence above
// seq, oldest first, until none are left or fn returns an error.
//
// It makes one Direct Get per delta (the client has no batch helper). That's
// fine while snapshots keep the tail short; batch Direct Get is the upgrade.
func DeltasAfter(ctx context.Context, js jetstream.JetStream, projectID string, seq uint64, fn func(*jetstream.RawStreamMsg) error) error {
	subj, err := DocumentDelta(projectID)
	if err != nil {
		return err
	}
	stream, err := js.Stream(ctx, Documents.Name)
	if err != nil {
		return fmt.Errorf("deltas after: %w", err)
	}
	for {
		// With a subject, GetMsg returns the first message on it at or
		// after the given sequence.
		msg, err := stream.GetMsg(ctx, seq+1, jetstream.WithGetMsgSubject(subj))
		if errors.Is(err, jetstream.ErrMsgNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("deltas of %s after %d: %w", projectID, seq, err)
		}
		if err := fn(msg); err != nil {
			return err
		}
		seq = msg.Sequence
	}
}
