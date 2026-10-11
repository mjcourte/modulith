package storagemanager

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// blobRef identifies a stored object.
type blobRef struct {
	bucket, key string
}

type storedBlob struct {
	data        []byte
	contentType string
}

// grant is what presigned URL allows
type grant struct {
	method      string // PUT or GET
	ref         blobRef
	contentType string // PUT only
	size        int64  // PUT only
	expires     time.Time
}

// MemoryBlobs is an in-memory Blobs for testing and local runs
type MemoryBlobs struct {
	mu     sync.Mutex
	blobs  map[blobRef]storedBlob
	grants map[string]grant // URL token -> grant
}

func NewMemoryBlobs() *MemoryBlobs {
	return &MemoryBlobs{blobs: make(map[blobRef]storedBlob), grants: make(map[string]grant)}
}

// Compile-time check that *MemoryBlobs satisfies Blobs.
var _ Blobs = (*MemoryBlobs)(nil)

// Grant helpers
func (m *MemoryBlobs) sign(g grant) string {
	token := rand.Text()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.grants[token] = g
	u := url.URL{
		Scheme:   "memory",
		Host:     g.ref.bucket,
		Path:     "/" + g.ref.key,
		RawQuery: url.Values{"token": {token}}.Encode(),
	}
	return u.String()
}

// Blobs methods
func (m *MemoryBlobs) PresignPut(ctx context.Context, bucket, key, contentType string, size int64, ttl time.Duration) (string, error) {
	return m.sign(grant{
		method:      http.MethodPut,
		ref:         blobRef{bucket: bucket, key: key},
		contentType: contentType,
		size:        size,
		expires:     time.Now().Add(ttl),
	}), nil
}

func (m *MemoryBlobs) PresignGet(ctx context.Context, bucket, key string, ttl time.Duration) (string, error) {
	return m.sign(grant{
		method:  http.MethodGet,
		ref:     blobRef{bucket: bucket, key: key},
		expires: time.Now().Add(ttl),
	}), nil
}

func (m *MemoryBlobs) Stat(ctx context.Context, bucket, key string) (BlobInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.blobs[blobRef{bucket: bucket, key: key}]
	if !ok {
		return BlobInfo{}, ErrBlobNotFound
	}
	return BlobInfo{Size: int64(len(b.data)), ContentType: b.contentType}, nil
}

func (m *MemoryBlobs) Delete(ctx context.Context, bucket, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.blobs, blobRef{bucket: bucket, key: key})
	return nil
}

// Test helpers: these play the browser, using presigned URLs the way a
// client would. Real code never calls them.

// PutSigned uploads data to a presigned PUT URL, enforcing what was signed:
// the method, the expiry, the content type and the exact size.
func (m *MemoryBlobs) PutSigned(rawURL, contentType string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	g, err := m.lookupGrant(rawURL, http.MethodPut)
	if err != nil {
		return err
	}
	switch {
	case contentType != g.contentType:
		return fmt.Errorf("presigned put: content type %q, signed for %q", contentType, g.contentType)
	case int64(len(data)) != g.size:
		return fmt.Errorf("presigned put: %d bytes, signed for %d", len(data), g.size)
	}
	// Store a copy: the caller may reuse data afterwards, which S3 could never see.
	m.blobs[g.ref] = storedBlob{data: bytes.Clone(data), contentType: contentType}
	return nil
}

// lookupGrant returns the grant behind a presigned URL if it allows method
// right now. The caller must hold m.mu (mutexes aren't re-entrant, so this
// doesn't lock itself).
func (m *MemoryBlobs) lookupGrant(rawURL, method string) (grant, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return grant{}, fmt.Errorf("parse presigned url: %w", err)
	}
	// A missing token gives "", which is never a key, so one lookup covers both cases.
	g, ok := m.grants[u.Query().Get("token")]
	switch {
	case !ok:
		return grant{}, errors.New("presigned url: unknown or missing token")
	case g.method != method:
		return grant{}, fmt.Errorf("presigned url: signed for %s, used for %s", g.method, method)
	case time.Now().After(g.expires):
		return grant{}, fmt.Errorf("presigned url: expired at %s", g.expires.Format(time.RFC3339))
	}
	return g, nil
}

func (m *MemoryBlobs) GetSigned(rawURL string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g, err := m.lookupGrant(rawURL, http.MethodGet)
	if err != nil {
		return nil, err
	}
	b, ok := m.blobs[g.ref]
	if !ok {
		return nil, ErrBlobNotFound
	}
	return bytes.Clone(b.data), nil
}
