package snapshots

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gyoza/internal/engine"
)

var (
	// ErrNotFound: the project has no snapshot, or the ref names none.
	ErrNotFound = errors.New("snapshots: not found")
	// ErrInvalid: the input can't be stored (no project, unknown format).
	ErrInvalid = errors.New("snapshots: invalid")
	// ErrStale: Put was given a snapshot covering an earlier stream sequence
	// than the project's latest. Storing it would make it Latest, and loads
	// would replay from a sequence whose deltas may already be compacted away.
	ErrStale = errors.New("snapshots: stale")
	// ErrNoProject: storage knows no such project (never created, or deleted),
	// so its document can't be stored. Retrying won't help.
	ErrNoProject = errors.New("snapshots: no such project")
)

// Ref identifies a stored snapshot.
type Ref struct {
	ProjectID string
	ObjectID  string // storage's ledger object ID
	Version   int    // 1 for a project's first snapshot, then +1 per Put
	Seq       uint64 // last stream sequence the snapshot covers
	// The last change it holds: who made it (empty if unknown) and when.
	EditedBy string
	EditedAt time.Time
	// ContentType is the snapshot's MIME type as stored (ContentType): it
	// gives Get the wire format and version back.
	ContentType string
}

// Edit is the last change a snapshot holds (live.Edit, as Put takes it).
type Edit struct {
	By string
	At time.Time
}

// Store keeps every snapshot of every project; Latest is the one with the
// highest Version. Put never stores a snapshot covering an earlier stream
// sequence than the latest (ErrStale), so Latest's Seq never goes back; the
// check and the store are atomic. Equal sequences are allowed: a later save
// at the same sequence can hold more local edits. Stream sequences are
// assigned by NATS, one total order, unlike replicas' Lamport clocks.
// Implementations are safe for concurrent use.
type Store interface {
	// Durable reports whether a stored snapshot survives this process (S3
	// through storage). Only then may the deltas it covers be compacted out
	// of the stream: deltas plus durable snapshots must always rebuild the
	// latest state after a crash.
	Durable() bool
	Put(ctx context.Context, projectID string, snap engine.Snapshot, seq uint64, last Edit) (Ref, error)
	Latest(ctx context.Context, projectID string) (Ref, error)
	// Version returns one of the project's stored versions (ErrNotFound if
	// it was never stored or retention removed it), e.g. to revert to.
	Version(ctx context.Context, projectID string, version int) (Ref, error)
	Get(ctx context.Context, ref Ref) (engine.Snapshot, error)
}

// Content types per wire format, as storage records them in its ledger
// (Kind=snapshot). The engine version travels in the type's parameter.
var contentTypes = map[engine.WireFormat]string{
	engine.WireNative: "application/vnd.gyoza.app+flatbuffers",
	engine.WireJSON:   "application/json",
}

// ContentType returns the MIME type for a snapshot's format and version.
func ContentType(f engine.WireFormat, version int) (string, error) {
	base, ok := contentTypes[f]
	if !ok {
		return "", fmt.Errorf("%w: format %q", ErrInvalid, f)
	}
	return fmt.Sprintf("%s; version=%d", base, version), nil
}

// ParseContentType is ContentType's inverse, for a snapshot read back.
func ParseContentType(contentType string) (engine.WireFormat, int, error) {
	base, params, _ := strings.Cut(contentType, ";")
	base = strings.TrimSpace(base)
	for f, t := range contentTypes {
		if t != base {
			continue
		}
		v, ok := strings.CutPrefix(strings.TrimSpace(params), "version=")
		version, err := strconv.Atoi(v)
		if !ok || err != nil {
			return "", 0, fmt.Errorf("%w: no version in %q", ErrInvalid, contentType)
		}
		return f, version, nil
	}
	return "", 0, fmt.Errorf("%w: not a snapshot type: %q", ErrInvalid, contentType)
}

// checkPut is the validation every Store applies before storing.
func checkPut(projectID string, snap engine.Snapshot) error {
	if projectID == "" {
		return fmt.Errorf("%w: no project", ErrInvalid)
	}
	if _, err := ContentType(snap.Format, snap.Version); err != nil {
		return err
	}
	if len(snap.Payload) == 0 {
		return fmt.Errorf("%w: empty snapshot", ErrInvalid)
	}
	return nil
}
