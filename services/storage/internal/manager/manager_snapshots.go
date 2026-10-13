package manager

// Document snapshots, service to service (the documents service; never the
// gateway). Two-phase like uploads: BeginSnapshot records a pending row and
// presigns a PUT; CompleteSnapshot checks the object in S3, records it in the
// ledger and gives it the project's next version, refusing to let the latest
// go back to an earlier stream sequence (ErrSnapshotStale).

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"gyoza/services/storage/internal/blob"
	"gyoza/services/storage/internal/ledger"
	"gyoza/services/storage/internal/snapshots"
)

// Errors for snapshots; ErrForbidden covers an unknown project.
var (
	ErrSnapshotStale    = snapshots.ErrStale
	ErrSnapshotNotFound = snapshots.ErrNotFound
	ErrInvalidSnapshot  = snapshots.ErrInvalid
	ErrSnapshotTooLarge = errors.New("storage: snapshot too large")
	ErrSnapshotExpired  = errors.New("storage: snapshot upload expired")
)

// snapshotTypes are the content types a snapshot may have, before any
// parameters (documents adds "; version=N").
var snapshotTypes = []string{"application/vnd.gyoza.app+flatbuffers", "application/json"}

// SnapshotRequest describes a snapshot documents is about to upload.
type SnapshotRequest struct {
	ProjectID   string
	Seq         uint64 // last delta stream sequence it covers
	EditedBy    string // who made the last change it holds (may be empty)
	EditedAt    time.Time
	ContentType string
	SizeBytes   int64
}

// SnapshotTicket is where to PUT the snapshot, then pass ObjectID to
// CompleteSnapshot.
type SnapshotTicket struct {
	ObjectID  string
	URL       string            // presigned PUT
	Headers   map[string]string // must be sent as-is (Content-Type is signed)
	ExpiresAt time.Time
}

// SnapshotRef describes a stored snapshot.
type SnapshotRef struct {
	ProjectID   string
	ObjectID    string
	Version     int
	Seq         uint64
	EditedBy    string
	EditedAt    time.Time
	ContentType string
	SizeBytes   int64
	CreatedAt   time.Time // when it was completed
}

// SnapshotPage is one page of ListSnapshots, newest first.
type SnapshotPage struct {
	Snapshots     []SnapshotRef
	NextPageToken string // "" on the last page
}

const snapshotPageSize = 50

func (m *Manager) BeginSnapshot(ctx context.Context, req SnapshotRequest) (SnapshotTicket, error) {
	switch {
	case req.SizeBytes < 1:
		return SnapshotTicket{}, fmt.Errorf("%w: size %d", ErrInvalidSnapshot, req.SizeBytes)
	case req.SizeBytes > m.cfg.MaxSnapshotBytes:
		return SnapshotTicket{}, fmt.Errorf("%w: %d bytes, the limit is %d", ErrSnapshotTooLarge, req.SizeBytes, m.cfg.MaxSnapshotBytes)
	case !isSnapshotType(req.ContentType):
		return SnapshotTicket{}, fmt.Errorf("%w: content type %q", ErrInvalidSnapshot, req.ContentType)
	}
	// An unknown project, or one being deleted, is refused (ErrForbidden).
	if _, err := m.authz.ProjectOwner(ctx, req.ProjectID); err != nil {
		return SnapshotTicket{}, err
	}
	id := uuid.Must(uuid.NewV7()).String()
	key := "projects/" + req.ProjectID + "/snapshot/" + id
	url, err := m.blobs.PresignPut(ctx, m.cfg.Bucket, key, req.ContentType, req.SizeBytes, m.cfg.UploadURLTTL)
	if err != nil {
		return SnapshotTicket{}, fmt.Errorf("presign snapshot upload: %w", err)
	}
	expires := m.now().Add(m.cfg.UploadURLTTL)
	_, err = m.snaps.Begin(ctx, snapshots.Snapshot{
		ObjectID: id, ProjectID: req.ProjectID, Seq: req.Seq,
		EditedBy: req.EditedBy, EditedAt: req.EditedAt,
		Bucket: m.cfg.Bucket, Key: key, ContentType: req.ContentType, SizeBytes: req.SizeBytes,
		ExpiresAt: expires,
	})
	if err != nil {
		return SnapshotTicket{}, err
	}
	return SnapshotTicket{
		ObjectID: id, URL: url,
		Headers:   map[string]string{"Content-Type": req.ContentType},
		ExpiresAt: expires,
	}, nil
}

// CompleteSnapshot records an uploaded snapshot. It returns only once the
// object is in S3 with the size and type begun, so the caller may then drop
// the deltas it covers. Completing again returns the same ref. A stale one is
// refused (ErrSnapshotStale) and its object removed.
func (m *Manager) CompleteSnapshot(ctx context.Context, projectID, objectID string) (SnapshotRef, error) {
	s, err := m.projectSnapshot(ctx, projectID, objectID)
	if err != nil {
		return SnapshotRef{}, err
	}
	if s.Version != 0 {
		return refOf(s), nil
	}
	if m.now().After(s.ExpiresAt) {
		return SnapshotRef{}, fmt.Errorf("%w: at %s", ErrSnapshotExpired, s.ExpiresAt.Format(time.RFC3339))
	}
	info, err := m.blobs.Stat(ctx, s.Bucket, s.Key)
	if errors.Is(err, blob.ErrNotFound) {
		return SnapshotRef{}, fmt.Errorf("%w: snapshot %s", ErrNotUploaded, objectID)
	}
	if err != nil {
		return SnapshotRef{}, fmt.Errorf("check snapshot upload: %w", err)
	}
	if info.Size != s.SizeBytes || info.ContentType != s.ContentType {
		return SnapshotRef{}, fmt.Errorf("%w: uploaded %d bytes of %q, begun as %d of %q",
			ErrInvalidSnapshot, info.Size, info.ContentType, s.SizeBytes, s.ContentType)
	}
	// The ledger first (idempotent on the object ID), then the version. If
	// the version is refused, the ledger row and object go again.
	_, err = m.ledger.Create(ctx, ledger.Object{
		ID:          objectID,
		Owner:       ledger.Owner{Kind: ledger.OwnerProject, ID: projectID},
		CreatedBy:   s.EditedBy,
		ProjectID:   projectID,
		Kind:        ledger.KindSnapshot,
		Bucket:      s.Bucket,
		Key:         s.Key,
		SizeBytes:   s.SizeBytes,
		ContentType: s.ContentType,
	})
	if err != nil {
		return SnapshotRef{}, fmt.Errorf("record snapshot in ledger: %w", err)
	}
	done, err := m.snaps.Complete(ctx, objectID)
	if errors.Is(err, ErrSnapshotStale) {
		if cerr := m.dropSnapshot(ctx, s); cerr != nil {
			return SnapshotRef{}, errors.Join(err, cerr) // cleanup retries when it expires
		}
		return SnapshotRef{}, err
	}
	if err != nil {
		return SnapshotRef{}, err
	}
	return refOf(done), nil
}

func (m *Manager) LatestSnapshot(ctx context.Context, projectID string) (SnapshotRef, error) {
	s, err := m.snaps.Latest(ctx, projectID)
	if err != nil {
		return SnapshotRef{}, err
	}
	return refOf(s), nil
}

// ListSnapshots lists a project's snapshots newest first, for reverts.
func (m *Manager) ListSnapshots(ctx context.Context, projectID, pageToken string) (SnapshotPage, error) {
	before := 0
	if pageToken != "" {
		v, err := strconv.Atoi(pageToken)
		if err != nil || v < 1 {
			return SnapshotPage{}, fmt.Errorf("%w: page token %q", ErrInvalidSnapshot, pageToken)
		}
		before = v
	}
	list, err := m.snaps.List(ctx, projectID, before, snapshotPageSize+1)
	if err != nil {
		return SnapshotPage{}, err
	}
	var page SnapshotPage
	for i, s := range list {
		if i == snapshotPageSize {
			page.NextPageToken = strconv.Itoa(list[i-1].Version)
			break
		}
		page.Snapshots = append(page.Snapshots, refOf(s))
	}
	return page, nil
}

// SnapshotURL returns a presigned GET for one of the project's completed
// snapshots.
func (m *Manager) SnapshotURL(ctx context.Context, projectID, objectID string) (Download, error) {
	s, err := m.projectSnapshot(ctx, projectID, objectID)
	if err != nil {
		return Download{}, err
	}
	if s.Version == 0 {
		return Download{}, fmt.Errorf("%w: snapshot %s isn't completed", ErrSnapshotNotFound, objectID)
	}
	obj, err := m.ledger.Get(ctx, objectID)
	if errors.Is(err, ledger.ErrNotFound) || (err == nil && obj.DeletedAt != nil) {
		return Download{}, fmt.Errorf("%w: snapshot %s", ErrSnapshotNotFound, objectID)
	}
	if err != nil {
		return Download{}, err
	}
	name := fmt.Sprintf("snapshot-v%d", s.Version)
	return m.presignDownload(ctx, obj, name)
}

// projectSnapshot gets a snapshot row and checks it belongs to the project,
// so one project's ID can't read another's snapshot.
func (m *Manager) projectSnapshot(ctx context.Context, projectID, objectID string) (snapshots.Snapshot, error) {
	s, err := m.snaps.Get(ctx, objectID)
	if err != nil {
		return snapshots.Snapshot{}, err
	}
	if s.ProjectID != projectID {
		return snapshots.Snapshot{}, fmt.Errorf("%w: %s", ErrSnapshotNotFound, objectID)
	}
	return s, nil
}

// dropSnapshot removes a snapshot's object, ledger row and row. Each step
// tolerates what's already gone, so it can be retried.
func (m *Manager) dropSnapshot(ctx context.Context, s snapshots.Snapshot) error {
	if err := m.blobs.Delete(ctx, s.Bucket, s.Key); err != nil {
		return fmt.Errorf("delete snapshot %s: %w", s.ObjectID, err)
	}
	if err := m.purgeObject(ctx, s.ObjectID); err != nil {
		return err
	}
	return m.snaps.Delete(ctx, s.ObjectID)
}

func isSnapshotType(contentType string) bool {
	base, _, _ := strings.Cut(contentType, ";")
	for _, t := range snapshotTypes {
		if strings.TrimSpace(base) == t {
			return true
		}
	}
	return false
}

func refOf(s snapshots.Snapshot) SnapshotRef {
	r := SnapshotRef{
		ProjectID: s.ProjectID, ObjectID: s.ObjectID, Version: s.Version, Seq: s.Seq,
		EditedBy: s.EditedBy, EditedAt: s.EditedAt,
		ContentType: s.ContentType, SizeBytes: s.SizeBytes,
	}
	if s.CompletedAt != nil {
		r.CreatedAt = *s.CompletedAt
	}
	return r
}
