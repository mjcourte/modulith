package snapshots

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/mjcourte/modulith/internal/engine"
	"github.com/mjcourte/modulith/platform/telemetry"
	"github.com/mjcourte/modulith/services/storage"
)

// Storage is what StorageStore needs from the storage service: its snapshot
// calls. *storage.Manager (monolith) and storage's gRPC client
// (distributed) both have them.
type Storage interface {
	BeginSnapshot(ctx context.Context, req storage.SnapshotRequest) (storage.SnapshotTicket, error)
	CompleteSnapshot(ctx context.Context, projectID, objectID string) (storage.SnapshotRef, error)
	LatestSnapshot(ctx context.Context, projectID string) (storage.SnapshotRef, error)
	ListSnapshots(ctx context.Context, projectID, pageToken string) (storage.SnapshotPage, error)
	SnapshotURL(ctx context.Context, projectID, objectID string) (storage.Download, error)
}

// Transfer moves the bytes to and from presigned URLs: HTTP in production
// (HTTPTransfer), storage.Memory's fake in tests.
type Transfer interface {
	Put(ctx context.Context, url string, headers map[string]string, data []byte) error
	Get(ctx context.Context, url string) ([]byte, error)
}

// StorageStore is the durable Store: snapshots in S3 through the storage
// service. The bytes go straight to S3 by presigned URL, never through
// storage itself.
type StorageStore struct {
	storage  Storage
	transfer Transfer
}

func NewStorageStore(s Storage, t Transfer) *StorageStore {
	return &StorageStore{storage: s, transfer: t}
}

var _ Store = (*StorageStore)(nil)

// Durable: CompleteSnapshot returns only once S3 holds the object.
func (s *StorageStore) Durable() bool { return true }

func (s *StorageStore) Put(ctx context.Context, projectID string, snap engine.Snapshot, seq uint64, last Edit) (Ref, error) {
	if err := checkPut(projectID, snap); err != nil {
		return Ref{}, err
	}
	ct, _ := ContentType(snap.Format, snap.Version) // checked by checkPut
	ticket, err := s.storage.BeginSnapshot(ctx, storage.SnapshotRequest{
		ProjectID: projectID, Seq: seq, EditedBy: last.By, EditedAt: last.At,
		ContentType: ct, SizeBytes: int64(len(snap.Payload)),
	})
	if err != nil {
		return Ref{}, fromStorage(err)
	}
	if err := s.transfer.Put(ctx, ticket.URL, ticket.Headers, snap.Payload); err != nil {
		return Ref{}, fmt.Errorf("snapshots: upload: %w", err)
	}
	ref, err := s.storage.CompleteSnapshot(ctx, projectID, ticket.ObjectID)
	if err != nil {
		return Ref{}, fromStorage(err)
	}
	return refOf(ref), nil
}

func (s *StorageStore) Latest(ctx context.Context, projectID string) (Ref, error) {
	ref, err := s.storage.LatestSnapshot(ctx, projectID)
	if err != nil {
		return Ref{}, fromStorage(err)
	}
	return refOf(ref), nil
}

func (s *StorageStore) Get(ctx context.Context, ref Ref) (engine.Snapshot, error) {
	dl, err := s.storage.SnapshotURL(ctx, ref.ProjectID, ref.ObjectID)
	if err != nil {
		return engine.Snapshot{}, fromStorage(err)
	}
	format, version, err := ParseContentType(ref.ContentType)
	if err != nil {
		return engine.Snapshot{}, err
	}
	data, err := s.transfer.Get(ctx, dl.URL)
	if err != nil {
		return engine.Snapshot{}, fmt.Errorf("snapshots: download: %w", err)
	}
	return engine.Snapshot{Format: format, Version: version, Payload: data}, nil
}

func refOf(r storage.SnapshotRef) Ref {
	return Ref{
		ProjectID: r.ProjectID, ObjectID: r.ObjectID, Version: r.Version, Seq: r.Seq,
		EditedBy: r.EditedBy, EditedAt: r.EditedAt, ContentType: r.ContentType,
	}
}

// fromStorage adds this package's sentinel to storage's, keeping both.
func fromStorage(err error) error {
	switch {
	case errors.Is(err, storage.ErrSnapshotStale):
		return fmt.Errorf("%w: %w", ErrStale, err)
	case errors.Is(err, storage.ErrForbidden):
		// BeginSnapshot refuses only an unknown project, or one being deleted.
		return fmt.Errorf("%w: %w", ErrNoProject, err)
	case errors.Is(err, storage.ErrSnapshotNotFound):
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	case errors.Is(err, storage.ErrInvalidSnapshot), errors.Is(err, storage.ErrSnapshotTooLarge):
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return err
}

// HTTPTransfer moves snapshot bytes over HTTP.
type HTTPTransfer struct {
	Client *http.Client
	// MaxBytes bounds a download; storage refuses larger snapshots anyway.
	MaxBytes int64
}

// NewHTTPTransfer returns a transfer with a timeout fit for snapshots up to
// storage's cap (32 MiB by default).
func NewHTTPTransfer(maxBytes int64) *HTTPTransfer {
	return &HTTPTransfer{Client: &http.Client{Timeout: 2 * time.Minute, Transport: telemetry.HTTPTransport(nil)}, MaxBytes: maxBytes}
}

func (t *HTTPTransfer) Put(ctx context.Context, url string, headers map[string]string, data []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.ContentLength = int64(len(data))
	resp, err := t.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
		return fmt.Errorf("PUT: %s: %s", resp.Status, body)
	}
	return nil
}

func (t *HTTPTransfer) Get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := t.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("GET: %s", resp.Status)
	}
	// One byte over the limit tells "too big" from "exactly the limit".
	data, err := io.ReadAll(io.LimitReader(resp.Body, t.MaxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > t.MaxBytes {
		return nil, fmt.Errorf("GET: snapshot over %d bytes", t.MaxBytes)
	}
	return data, nil
}

// Version lists from just above version (the page token is "before this
// version"), so the first entry is the one asked for, if it's still stored.
func (s *StorageStore) Version(ctx context.Context, projectID string, version int) (Ref, error) {
	if version < 1 {
		return Ref{}, fmt.Errorf("%w: version %d", ErrNotFound, version)
	}
	page, err := s.storage.ListSnapshots(ctx, projectID, strconv.Itoa(version+1))
	if err != nil {
		return Ref{}, fromStorage(err)
	}
	if len(page.Snapshots) == 0 || page.Snapshots[0].Version != version {
		return Ref{}, fmt.Errorf("%w: version %d of %q", ErrNotFound, version, projectID)
	}
	return refOf(page.Snapshots[0]), nil
}
