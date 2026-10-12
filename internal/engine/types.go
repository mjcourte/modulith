package engine

import "time"

type Settings struct {
	ID      string
	Schema  string
	Options map[string]any
}

type Query struct {
	Format    QueryFormat
	Source    string
	Variables map[string]any
}

type QueryFormat string

const (
	QueryNative  QueryFormat = "native"
	QueryGraphQL QueryFormat = "graphql"
)

type View struct {
	// Canonical, typed result representation.
	// Implementation hidden behind the facade.
}

type WireFormat string

const (
	WireNative WireFormat = "native"
	WireJSON   WireFormat = "json"
)

type Snapshot struct {
	Format  WireFormat
	Version int
	Payload []byte
}

type DeltaInput struct {
	Format  WireFormat
	Version int
	Payload []byte
}

type Delta struct {
	Format  WireFormat
	Version int
	Payload []byte
}

type ImageOptions struct {
	Format string // png, jpeg, webp...
	Width  int
	Height int
}

type VideoOptions struct {
	Format   string // mp4, webm...
	FPS      float64
	Start    time.Duration
	Duration time.Duration
	Width    int
	Height   int
}

type RenderedImage struct {
	Data        []byte
	ContentType string
	Width       int
	Height      int
}

type RenderedVideo struct {
	Data        []byte
	ContentType string
	Duration    time.Duration
}

type AssetFilter struct {
	IDs   []string
	Types []string
}

type AssetRef struct {
	ID          string
	ObjectKey   string
	ContentType string
	SizeBytes   int64
}
