package nats

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// DocumentDeltas is the write-ahead log of document edits. Limits retention
// (not a work queue) so every consumer sees every delta: documentmanager and
// each gateway read the same messages, and a client can replay from the
// sequence its snapshot covers. Deltas are removed by PurgeDeltas once a
// snapshot covering them is in S3; MaxAge only catches deltas a crashed
// documentmanager never purged.
var DocumentDeltas = jetstream.StreamConfig{
	Name:       "DOCUMENT_DELTAS",
	Subjects:   []string{PatternDocumentDelta},
	Storage:    jetstream.FileStorage,
	Retention:  jetstream.LimitsPolicy,
	Discard:    jetstream.DiscardOld,
	MaxAge:     7 * 24 * time.Hour,
	MaxMsgSize: 256 << 10,
	// Publishes that set a Nats-Msg-Id are stored once within this window,
	// so a publisher can retry after a timeout without doubling an edit.
	Duplicates: 2 * time.Minute,
	Replicas:   3,
}

// DocumentSnapshots announces the latest snapshot per project. Messages are
// pointers (version, last delta sequence covered, storage object ID), not
// the snapshot itself, which can be far bigger than a NATS message. Only the
// latest per project matters, so older ones are dropped.
var DocumentSnapshots = jetstream.StreamConfig{
	Name:              "DOCUMENT_SNAPSHOTS",
	Subjects:          []string{PatternDocumentSnapshot},
	Storage:           jetstream.FileStorage,
	Retention:         jetstream.LimitsPolicy,
	Discard:           jetstream.DiscardOld,
	MaxMsgsPerSubject: 1,
	MaxMsgSize:        4 << 10,
	Replicas:          3,
}

// Streams is every stream Gyoza uses. Their subjects must not overlap: the
// server rejects a stream whose subjects another stream already covers.
var Streams = []jetstream.StreamConfig{DocumentDeltas, DocumentSnapshots}

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

// PurgeDeltas removes a project's deltas up to and including seq, the last
// stream sequence a stored snapshot covers. Purging the same range twice is
// harmless.
func PurgeDeltas(ctx context.Context, js jetstream.JetStream, projectID string, seq uint64) error {
	subj, err := DocumentDelta(projectID)
	if err != nil {
		return err
	}
	stream, err := js.Stream(ctx, DocumentDeltas.Name)
	if err != nil {
		return fmt.Errorf("purge deltas: %w", err)
	}
	// WithPurgeSequence keeps the message at the given sequence, hence +1.
	err = stream.Purge(ctx, jetstream.WithPurgeSubject(subj), jetstream.WithPurgeSequence(seq+1))
	if err != nil {
		return fmt.Errorf("purge deltas of %s: %w", projectID, err)
	}
	return nil
}
