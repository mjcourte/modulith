// Package blob is file storage addressed by bucket and key: S3 in production,
// an in-memory fake (Memory) in tests and local runs.
//
// It's mechanism only: callers decide keys, buckets and how long URLs live.
// Browsers talk to storage directly through presigned URLs, so servers never
// carry file bytes. Every Store implementation must pass blobtest.RunSuite.
package blob

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound means nothing is stored at a bucket/key.
var ErrNotFound = errors.New("blob: not found")

// errCopyOntoItself refuses a Copy whose source and destination are the same.
var errCopyOntoItself = errors.New("blob: copy onto itself")

// Store is file storage addressed by bucket and key.
type Store interface {
	// PresignPut returns a URL the browser can PUT exactly size bytes of
	// contentType to, valid for ttl.
	PresignPut(ctx context.Context, bucket, key, contentType string, size int64, ttl time.Duration) (string, error)
	// PresignGet returns a URL the browser can GET, valid for ttl. If filename
	// isn't empty, the download is served as an attachment with that name
	// (S3: a signed Content-Disposition override), so it saves as "Logo.png"
	// rather than under the key. Presigning doesn't check the object exists.
	PresignGet(ctx context.Context, bucket, key string, ttl time.Duration, filename string) (string, error)
	// Stat reports what was actually uploaded, or ErrNotFound if nothing was.
	Stat(ctx context.Context, bucket, key string) (Info, error)
	// ReadPrefix returns the first n bytes of the object (fewer if it's
	// shorter), or ErrNotFound. Used to check a file's real type from its
	// first bytes without downloading it (S3: a Range request).
	ReadPrefix(ctx context.Context, bucket, key string, n int) ([]byte, error)
	// Delete removes the object. Deleting a missing object is not an error (as in S3).
	Delete(ctx context.Context, bucket, key string) error
	// Copy copies the object at srcKey to dstKey in the same bucket, server-side:
	// the bytes never pass through us. The copy keeps the source's content type.
	// A missing source is ErrNotFound. An existing dstKey is overwritten, so a
	// retried copy is harmless. srcKey and dstKey must differ. (S3: one
	// CopyObject, which takes up to 5 GiB, the upload limit.)
	Copy(ctx context.Context, bucket, srcKey, dstKey string) error
}

// Info is what Stat reports about a stored object.
type Info struct {
	Size        int64
	ContentType string
}
