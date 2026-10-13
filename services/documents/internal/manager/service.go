package manager

import (
	"context"
	"errors"

	"gyoza/internal/engine"
)

// ErrExists is returned by InitDocument for a project that already has a
// document.
var ErrExists = errors.New("documents: document already exists")

// ErrUsesNotRecorded is returned by UpdateDocument, together with the delta,
// when the edit was applied but storage's usage rows couldn't be updated.
// The edit must still be distributed; the next edit retries the rows.
var ErrUsesNotRecorded = errors.New("documents: edit applied, asset uses not recorded")

// ErrNotPublished is returned by UpdateDocument, together with the delta, when
// the edit was applied here but couldn't be published to the stream. Other
// replicas get it from the next snapshot; the caller may publish the
// returned delta itself (it's deduplicated by content).
var ErrNotPublished = errors.New("documents: edit applied, not published")

// Service is what other components call: the gateway (for a signed-in user,
// passing their ID as actorID) and, later, iam (cloning). *Manager
// implements it in process; callers should depend on Service.
//
// Engine errors (engine.ErrUnsupportedFormat for a delta in the wrong wire
// format) pass through unchanged.
type Service interface {
	// GetDocument returns the document's current state, creating it first
	// if the project has none.
	GetDocument(ctx context.Context, actorID, projectID string) (Document, error)
	// QueryDocument reads part of the document as a JSON view.
	QueryDocument(ctx context.Context, actorID, projectID string, q engine.Query) (*engine.View, error)
	// InitDocument creates the project's document (stored as version 1) and
	// returns it, or ErrExists.
	InitDocument(ctx context.Context, actorID, projectID string) (Document, error)
	// UpdateDocument applies an edit and returns it stamped, for
	// distribution to the other replicas. See ErrNotPublished and
	// ErrUsesNotRecorded.
	UpdateDocument(ctx context.Context, actorID, projectID string, delta engine.DeltaInput) (engine.Delta, error)
	// RevertDocument puts the document back to a stored version as one fresh
	// edit, returned like UpdateDocument's. Administrative (the admin tool):
	// needs write access, until iam has a staff flag.
	RevertDocument(ctx context.Context, actorID, projectID string, version int) (engine.Delta, error)
}

// Document is a document's state as a snapshot, with the last delta stream
// sequence it covers: a client applies only deltas after Seq.
type Document struct {
	Snapshot engine.Snapshot
	Seq      uint64
}

var _ Service = (*Manager)(nil)
