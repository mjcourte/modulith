package manager

import (
	"errors"
	"fmt"
	"time"

	"gyoza/platform/blob"
	"gyoza/services/storage/internal/ledger"
)

// Errors returned by Manager methods, besides those from the Store,
// Authorizer and blob store it calls.
var (
	ErrUploadTooLarge = errors.New("storage: upload too large")
	ErrNotUploaded    = errors.New("storage: file not uploaded yet")
)

// Defaults and hard limits for Config.
const (
	DefaultUploadURLTTL   = 15 * time.Minute
	DefaultDownloadURLTTL = 5 * time.Minute
	DefaultMaxUploadBytes = 1 << 30 // 1 GiB
	DefaultTrashRetention = 7 * 24 * time.Hour
	// MaxSinglePutBytes is S3's limit for one PUT (5 GiB). Bigger files need
	// multipart uploads, which aren't supported yet.
	MaxSinglePutBytes = 5 << 30
	// MaxPresignTTL is the longest S3 presigned URLs can live (7 days).
	MaxPresignTTL = 7 * 24 * time.Hour
)

// Config is the Manager's policy: where files go and how long URLs live.
// Zero values mean "use the default", except Bucket, which is required.
type Config struct {
	Bucket         string
	UploadURLTTL   time.Duration // how long a presigned upload URL works; default 15m
	DownloadURLTTL time.Duration // how long a presigned download URL works; default 5m
	MaxUploadBytes int64         // largest file accepted; default 1 GiB, at most 5 GiB
	// TrashRetention is how long trashed files can be restored before the
	// cleanup job deletes them for good; default 7 days. Also how long
	// completed upload sessions and other deleted ledger objects are kept.
	TrashRetention time.Duration
}

// withDefaults fills in zero values and checks the result.
func (c Config) withDefaults() (Config, error) {
	if c.Bucket == "" {
		return c, errors.New("storage config: Bucket is required")
	}
	if c.UploadURLTTL == 0 {
		c.UploadURLTTL = DefaultUploadURLTTL
	}
	if c.DownloadURLTTL == 0 {
		c.DownloadURLTTL = DefaultDownloadURLTTL
	}
	if c.MaxUploadBytes == 0 {
		c.MaxUploadBytes = DefaultMaxUploadBytes
	}
	if c.TrashRetention == 0 {
		c.TrashRetention = DefaultTrashRetention
	}
	if c.TrashRetention < time.Hour {
		// Shorter would leave restores almost no time, given the clock-skew margin.
		return c, fmt.Errorf("storage config: TrashRetention %s must be at least 1h", c.TrashRetention)
	}
	for name, ttl := range map[string]time.Duration{"UploadURLTTL": c.UploadURLTTL, "DownloadURLTTL": c.DownloadURLTTL} {
		if ttl < 0 || ttl > MaxPresignTTL {
			return c, fmt.Errorf("storage config: %s %s must be between 0 and %s", name, ttl, MaxPresignTTL)
		}
	}
	if c.MaxUploadBytes < 0 || c.MaxUploadBytes > MaxSinglePutBytes {
		return c, fmt.Errorf("storage config: MaxUploadBytes %d must be between 1 and %d", c.MaxUploadBytes, int64(MaxSinglePutBytes))
	}
	return c, nil
}

// Manager is storage's business logic: it checks permissions, moves
// files in and out of S3 via presigned URLs, records every stored object in
// the ledger, and keeps the DAM in step. It depends only on interfaces, so
// the monolith passes in-process implementations, distributed mode passes
// gRPC clients, and tests pass memory fakes.
//
// Every method takes the acting user's ID; the Authorizer decides what they
// may do. Safe for concurrent use.
type Manager struct {
	ledger ledger.Service
	store  Store
	blobs  blob.Store
	authz  Authorizer
	cfg    Config
	now    func() time.Time // the clock; tests replace it (export_test.go)
}

// NewManager wires a Manager. All dependencies are required.
func NewManager(l ledger.Service, store Store, blobs blob.Store, authz Authorizer, cfg Config) (*Manager, error) {
	if l == nil || store == nil || blobs == nil || authz == nil {
		return nil, errors.New("storage: NewManager needs a ledger, store, blobs and authorizer")
	}
	cfg, err := cfg.withDefaults()
	if err != nil {
		return nil, err
	}
	return &Manager{ledger: l, store: store, blobs: blobs, authz: authz, cfg: cfg, now: time.Now}, nil
}

// Config returns the effective configuration, with defaults filled in.
func (m *Manager) Config() Config { return m.cfg }
