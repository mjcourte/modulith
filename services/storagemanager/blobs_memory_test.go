package storagemanager_test

import (
	"testing"
	"time"

	"gyoza/services/storagemanager"
)

func TestMemoryBlobs(t *testing.T) {
	runBlobsTests(t, func(*testing.T) blobsHarness {
		b := storagemanager.NewMemoryBlobs()
		return blobsHarness{
			blobs:  b,
			bucket: "test",
			// Method values: b.PutSigned is a func with b already bound as the receiver.
			put:    b.PutSigned,
			get:    b.GetSigned,
			minTTL: 10 * time.Millisecond,
		}
	})
}
