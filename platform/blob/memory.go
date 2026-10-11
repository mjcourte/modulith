package blob

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

// grant is what a presigned URL allows.
type grant struct {
	method      string // PUT or GET
	ref         blobRef
	contentType string // PUT only
	size        int64  // PUT only
	filename    string // GET only: the name to save the download as ("" = none)
	expires     time.Time
}

// Memory is an in-memory Store for tests and local runs. Presigned URLs use a
// memory:// scheme no browser can open; PutSigned and GetSigned play the
// browser instead, enforcing what each URL was signed for, as S3 would.
type Memory struct {
	mu     sync.Mutex
	blobs  map[blobRef]storedBlob
	grants map[string]grant // URL token -> grant
}

// NewMemory returns an empty Memory.
func NewMemory() *Memory {
	return &Memory{blobs: make(map[blobRef]storedBlob), grants: make(map[string]grant)}
}

// Compile-time check that *Memory satisfies Store.
var _ Store = (*Memory)(nil)

// sign records g under a new token and returns the URL that carries it.
func (m *Memory) sign(g grant) string {
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

func (m *Memory) PresignPut(ctx context.Context, bucket, key, contentType string, size int64, ttl time.Duration) (string, error) {
	return m.sign(grant{
		method:      http.MethodPut,
		ref:         blobRef{bucket: bucket, key: key},
		contentType: contentType,
		size:        size,
		expires:     time.Now().Add(ttl),
	}), nil
}

func (m *Memory) PresignGet(ctx context.Context, bucket, key string, ttl time.Duration, filename string) (string, error) {
	return m.sign(grant{
		method:   http.MethodGet,
		ref:      blobRef{bucket: bucket, key: key},
		filename: filename,
		expires:  time.Now().Add(ttl),
	}), nil
}

func (m *Memory) Stat(ctx context.Context, bucket, key string) (Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.blobs[blobRef{bucket: bucket, key: key}]
	if !ok {
		return Info{}, ErrNotFound
	}
	return Info{Size: int64(len(b.data)), ContentType: b.contentType}, nil
}

func (m *Memory) ReadPrefix(ctx context.Context, bucket, key string, n int) ([]byte, error) {
	if n < 1 {
		return nil, fmt.Errorf("read prefix: n must be positive, got %d", n)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.blobs[blobRef{bucket: bucket, key: key}]
	if !ok {
		return nil, ErrNotFound
	}
	// min is a built-in (Go 1.21+); a shorter file just returns all of it.
	return bytes.Clone(b.data[:min(n, len(b.data))]), nil
}

func (m *Memory) Delete(ctx context.Context, bucket, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.blobs, blobRef{bucket: bucket, key: key})
	return nil
}

// Test helpers: these play the browser, using presigned URLs the way a
// client would. Real code never calls them.

// PutSigned uploads data to a presigned PUT URL, enforcing what was signed:
// the method, the expiry, the content type and the exact size.
func (m *Memory) PutSigned(rawURL, contentType string, data []byte) error {
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
func (m *Memory) lookupGrant(rawURL, method string) (grant, error) {
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

// GetSigned downloads from a presigned GET URL, enforcing its method and
// expiry. It returns the bytes and the filename the browser would save them
// under ("" if none was signed), or ErrNotFound, as S3 would 404.
func (m *Memory) GetSigned(rawURL string) (data []byte, filename string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g, err := m.lookupGrant(rawURL, http.MethodGet)
	if err != nil {
		return nil, "", err
	}
	b, ok := m.blobs[g.ref]
	if !ok {
		return nil, "", ErrNotFound
	}
	return bytes.Clone(b.data), g.filename, nil
}
