package engine

import "context"

type Document struct {
	// Private engine handle and document state.
}

func New(ctx context.Context, settings Settings) (*Document, error)

func (d *Document) ID() string

// Queries
func (d *Document) Query(
	ctx context.Context,
	query Query,
) (*View, error)

// CRDT operations
func (d *Document) Merge(
	ctx context.Context,
	snapshot Snapshot,
) error

func (d *Document) Edit(
	ctx context.Context,
	delta DeltaInput,
) (Delta, error)

func (d *Document) Snapshot(
	ctx context.Context,
) (Snapshot, error)

// Rendering
func (d *Document) RenderImage(
	ctx context.Context,
	query Query,
	opts ImageOptions,
) (RenderedImage, error)

func (d *Document) RenderVideo(
	ctx context.Context,
	query Query,
	opts VideoOptions,
) (RenderedVideo, error)

// Asset management
func (d *Document) LoadAssets(
	ctx context.Context,
	filter AssetFilter,
) error

func (d *Document) UnloadAssets(
	ctx context.Context,
	filter AssetFilter,
) error

func (d *Document) Assets(
	ctx context.Context,
	filter AssetFilter,
) ([]AssetRef, error)

func (d *Document) Close() error
