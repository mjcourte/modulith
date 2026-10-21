package nats

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// Documents carries all document traffic for every project: the deltas
// (the write-ahead log) and the snapshot pointers. Limits retention (not a
// work queue) so every consumer sees every message. There is one consumer
// per service instance, never one per project or client: per-project reads
// use Direct Get (documents.go), and fan-out to clients happens after the
// consumer (the gateway).
//
// Compact removes what a stored snapshot covers. MaxAge only catches what a
// crashed documents never compacted; it can also expire an idle project's
// last pointer, so readers fall back to storage's latest snapshot.
var Documents = jetstream.StreamConfig{
	Name:       "DOCUMENTS",
	Subjects:   []string{PatternDocumentDelta, PatternDocumentSnapshot},
	Storage:    jetstream.FileStorage,
	Retention:  jetstream.LimitsPolicy,
	Discard:    jetstream.DiscardOld,
	MaxAge:     7 * 24 * time.Hour,
	MaxMsgSize: 256 << 10,
	// Publishes that set a Nats-Msg-Id are stored once within this window,
	// so a publisher can retry after a timeout without doubling an edit.
	Duplicates: 2 * time.Minute,
	// Lets any replica answer Direct Get, the consumer-free read path.
	AllowDirect: true,
	Replicas:    3,
}

// Streams is every stream Modulith uses. Their subjects must not overlap: the
// server rejects a stream whose subjects another stream already covers.
var Streams = []jetstream.StreamConfig{Documents}

// EnsureStreams creates each stream in Streams, or updates it to match.
// Every binary calls it at startup, like a migration.
func EnsureStreams(ctx context.Context, js jetstream.JetStream) error {
	for _, cfg := range Streams {
		if _, err := js.CreateOrUpdateStream(ctx, cfg); err != nil {
			return fmt.Errorf("ensure stream %s: %w", cfg.Name, err)
		}
	}
	return nil
}

// Compact removes what a stored snapshot makes redundant: a project's deltas
// up to and including coveredSeq, and every snapshot pointer but the latest.
// Call it only after publishing the new pointer; repeating it is harmless.
func Compact(ctx context.Context, js jetstream.JetStream, projectID string, coveredSeq uint64) error {
	deltaSubj, err := DocumentDelta(projectID)
	if err != nil {
		return err
	}
	snapSubj, err := DocumentSnapshot(projectID)
	if err != nil {
		return err
	}
	stream, err := js.Stream(ctx, Documents.Name)
	if err != nil {
		return fmt.Errorf("compact: %w", err)
	}
	// WithPurgeSequence keeps the message at the given sequence, hence +1.
	if err := stream.Purge(ctx, jetstream.WithPurgeSubject(deltaSubj), jetstream.WithPurgeSequence(coveredSeq+1)); err != nil {
		return fmt.Errorf("compact deltas of %s: %w", projectID, err)
	}
	if err := stream.Purge(ctx, jetstream.WithPurgeSubject(snapSubj), jetstream.WithPurgeKeep(1)); err != nil {
		return fmt.Errorf("compact snapshots of %s: %w", projectID, err)
	}
	return nil
}
