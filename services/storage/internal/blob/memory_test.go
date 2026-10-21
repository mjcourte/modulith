package blob_test

import (
	"testing"
	"time"

	"github.com/mjcourte/modulith/services/storage/internal/blob"
	"github.com/mjcourte/modulith/services/storage/internal/blob/blobtest"
)

func TestMemory(t *testing.T) {
	blobtest.RunSuite(t, func(*testing.T) blobtest.Harness {
		m := blob.NewMemory()
		return blobtest.Harness{
			Store:  m,
			Bucket: "test",
			// Method values: m.PutSigned is a func with m already bound as the receiver.
			Put:    m.PutSigned,
			Get:    m.GetSigned,
			MinTTL: 10 * time.Millisecond,
		}
	})
}
