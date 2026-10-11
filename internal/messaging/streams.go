package messaging

import (
	"context"

	"github.com/nats-io/nats.go/jetstream"
)

// TODO pre-map our stream subjects!!
// how many Streams do we need? one omni stream?
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
