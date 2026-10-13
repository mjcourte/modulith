package snapshots

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var (
	ErrNotFound = errors.New("storage: snapshot not found")
	// ErrStale: the snapshot covers an earlier stream sequence than the
	// project's latest. Completing it would make loads replay from a
	// sequence whose deltas may already be compacted away.
	ErrStale   = errors.New("storage: snapshot is stale")
	ErrInvalid = errors.New("storage: invalid snapshot")
)

// MaxPageSize bounds List and the cleanup lists.
const MaxPageSize = 1000

// Snapshot is one row: pending (Version 0, CompletedAt nil) or completed.
type Snapshot struct {
	ObjectID  string
	ProjectID string
	Seq       uint64 // last delta stream sequence the snapshot covers
	Version   int    // 1 for a project's first completed snapshot; 0 while pending
	// The last change the snapshot holds: who made it and when (a `touch`).
	EditedBy    string
	EditedAt    time.Time
	Bucket, Key string
	ContentType string
	SizeBytes   int64
	ExpiresAt   time.Time // a pending snapshot past this is abandoned
	CreatedAt   time.Time
	CompletedAt *time.Time
}

// Store records snapshots. It checks data shape only; the Manager decides
// who may do what. Implementations are safe for concurrent use.
type Store interface {
	// Begin records a pending snapshot. ErrStale if its Seq is before the
	// project's latest (a cheap early refusal; Complete checks again).
	Begin(ctx context.Context, s Snapshot) (Snapshot, error)
	// Get returns a snapshot, pending or completed.
	Get(ctx context.Context, objectID string) (Snapshot, error)
	// Complete gives a pending snapshot the project's next version.
	// Completing it again returns it unchanged. ErrStale if its Seq is before
	// the project's latest; the check and the update are atomic per project.
	Complete(ctx context.Context, objectID string) (Snapshot, error)
	// Latest returns the project's completed snapshot with the highest version.
	Latest(ctx context.Context, projectID string) (Snapshot, error)
	// List returns up to limit completed snapshots newest first, with a
	// version below before (0: from the newest).
	List(ctx context.Context, projectID string, before, limit int) ([]Snapshot, error)
	// ListExpiredPending returns up to limit pending snapshots that expired
	// before the given time, oldest first: cleanup's work list.
	ListExpiredPending(ctx context.Context, before time.Time, limit int) ([]Snapshot, error)
	// ListBeyondKeep returns up to limit completed snapshots older than each
	// project's newest keep, oldest first: retention's work list.
	ListBeyondKeep(ctx context.Context, keep, limit int) ([]Snapshot, error)
	// Delete removes a row; a missing one is not an error.
	Delete(ctx context.Context, objectID string) error
	// DeleteProject removes every row of a deleted project (its objects are
	// trashed through the ledger). It returns how many it removed.
	DeleteProject(ctx context.Context, projectID string) (int, error)
	// ClearEditedBy removes userID as editor everywhere (GDPR, when the user
	// is deleted). It returns how many rows changed.
	ClearEditedBy(ctx context.Context, userID string) (int, error)
}

// checkBegin is the validation every Store applies to Begin.
func checkBegin(s Snapshot) error {
	switch {
	case s.ObjectID == "" || s.ProjectID == "":
		return fmt.Errorf("%w: object and project are required", ErrInvalid)
	case s.Bucket == "" || s.Key == "" || s.ContentType == "":
		return fmt.Errorf("%w: bucket, key and content type are required", ErrInvalid)
	case s.SizeBytes < 1:
		return fmt.Errorf("%w: size %d", ErrInvalid, s.SizeBytes)
	case s.Seq > 1<<63-1:
		return fmt.Errorf("%w: seq %d out of range", ErrInvalid, s.Seq)
	case s.ExpiresAt.IsZero():
		return fmt.Errorf("%w: no expiry", ErrInvalid)
	}
	return nil
}

func checkLimit(limit int) error {
	if limit < 1 || limit > MaxPageSize {
		return fmt.Errorf("%w: limit %d not in 1..%d", ErrInvalid, limit, MaxPageSize)
	}
	return nil
}

func stale(s Snapshot, latest Snapshot) error {
	return fmt.Errorf("%w: seq %d is before the latest's %d (version %d)", ErrStale, s.Seq, latest.Seq, latest.Version)
}

// now matches what Postgres stores: UTC, microsecond precision.
func now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }
