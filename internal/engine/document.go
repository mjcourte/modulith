package engine

import "context"

// Document is a live engine document. Implementations are safe for
// concurrent use; every method but ID returns ErrClosed after Close.
type Document interface {
	ID() string

	// Query returns a view; the query format is normalised behind the facade.
	Query(ctx context.Context, query Query) (*View, error)

	// Merge folds a snapshot into the document in place.
	Merge(ctx context.Context, snapshot Snapshot) error
	// Edit applies a delta in place and returns it in wire form, for NATS distribution.
	Edit(ctx context.Context, delta DeltaInput) (Delta, error)
	// Snapshot exports the document's full state.
	Snapshot(ctx context.Context) (Snapshot, error)

	RenderImage(ctx context.Context, query Query, opts ImageOptions) (RenderedImage, error)
	RenderVideo(ctx context.Context, query Query, opts VideoOptions) (RenderedVideo, error)

	// LoadAssets pulls matching assets into memory.
	LoadAssets(ctx context.Context, filter AssetFilter) error
	UnloadAssets(ctx context.Context, filter AssetFilter) error
	// Assets lists the matching assets the document references.
	Assets(ctx context.Context, filter AssetFilter) ([]AssetRef, error)
	// ReplaceAssetRefs points every use of each key at its value (clones,
	// forks) as one local edit, and returns that edit for distribution. Keys
	// the document doesn't use are ignored.
	ReplaceAssetRefs(ctx context.Context, refs map[AssetRef]AssetRef) (Delta, error)
	// RevertTo makes the document hold snap's state again, as one local edit
	// (never by going back in time): every key that differs is set back, and
	// every key snap didn't have is deleted, stamped with a fresh tick, so the
	// revert wins on every replica that applies it. It returns that edit for
	// distribution; an empty Payload if nothing differed.
	RevertTo(ctx context.Context, snap Snapshot) (Delta, error)

	Close() error
}

// Engine creates documents. Callers depend on an Engine, not a concrete
// implementation, so the mock (Mock) and the native engine are
// interchangeable.
type Engine interface {
	// New returns an empty document.
	New(ctx context.Context, settings Settings) (Document, error)
	// Load returns a document holding a snapshot's state: the way to open a
	// stored document. (Merge is for folding another replica's state into a
	// live document.) A snapshot it can't read is ErrUnsupportedFormat, one
	// with stamps from the future ErrBadStamp.
	Load(ctx context.Context, settings Settings, snap Snapshot) (Document, error)
}
