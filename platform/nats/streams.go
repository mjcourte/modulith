package nats

import (
	"context"

	"github.com/nats-io/nats.go/jetstream"
)

// TODO pre-map our stream subjects!!
// how many Streams do we need? one omni stream?

var DocumentDeltas = jetstream.StreamConfig{
	Name:      "DOCUMENT_DELTAS",
	Subjects:  []string{"document.*.delta"},
	Storage:   jetstream.FileStorage,
	Retention: jetstream.WorkQueuePolicy,
	Replicas:  3,
}

var DocumentSnapshots = jetstream.StreamConfig{
	Name:              "DOCUMENT_SNAPSHOTS",
	Subjects:          []string{"document.*.snapshot"},
	Storage:           jetstream.FileStorage,
	Retention:         jetstream.LimitsPolicy,
	Discard:           jetstream.DiscardOld,
	MaxMsgsPerSubject: 1,
	Replicas:          3,
}

func EnsureDocumentStream(
	ctx context.Context,
	js jetstream.JetStream,
) error {
	_, err := js.CreateOrUpdateStream(
		ctx,
		jetstream.StreamConfig{
			Name:     "DOCUMENTS",             // TODO rename
			Subjects: []string{"documents.>"}, // TODO go with rename
			Storage:  jetstream.FileStorage,
			Replicas: 3,
		},
	)
	return err
}
