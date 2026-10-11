package storagemanager

import (
	"context"
	"errors"
	"time"
)

// Blobs is the file storage addressed by bucket/key
type Blobs interface {
	// PresignPut returns a URL the browser can PUT exactly size bytes of ContentType to, valid for ttl.
	PresignPut(ctx context.Context, bucket, key, contentType string, size int64, ttl time.Duration) (string, error)
	// PresignGet returns a URL the browser can GET, valid for ttl.
	PresignGet(ctx context.Context, bucket, key string, ttl time.Duration) (string, error)
	// Stat reports what was actually uploaded, or ErrBlobNotFound if nothing was.
	Stat(ctx context.Context, bucket, key string) (BlobInfo, error)
	// Delete removes the object. Deleting a missing object is not an error (as in S3).
	Delete(ctx context.Context, bucket, key string) error
}

type BlobInfo struct {
	Size        int64
	ContentType string
}

var ErrBlobNotFound = errors.New("storagemanager: blob not found")
