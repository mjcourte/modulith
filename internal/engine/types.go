package engine

import (
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrClosed            = errors.New("engine: document closed")
	ErrUnsupportedFormat = errors.New("engine: unsupported format")
	ErrNotFound          = errors.New("engine: not found")
	// ErrBadStamp: a delta or snapshot carries a clock stamp too far in the
	// future to be honest (forged, or a badly skewed clock). Accepting it
	// would let it win every write and drag every replica's clock forward.
	ErrBadStamp = errors.New("engine: stamp from the future")
)

type Settings struct {
	ID string // the document (project) ID, the same on every replica
	// Replica identifies this copy of the document; it stamps local edits and
	// breaks ties between concurrent writes (newer, in mock.go), so it must be
	// unique per writer: the gateway assigns one per socket, a server's is per
	// process. Two writers sharing an ID can stamp different values alike, and
	// one of the two writes is then silently dropped.
	//
	// The ID is opaque bytes, compared bytewise. Never case-fold, normalise or
	// collate it: replicas would disagree about who won a tie and diverge
	// permanently. Empty picks a random one — 26 characters of RFC 4648 base32
	// (A-Z and 2-7, so ASCII by design and identical to compare in every
	// implementation), ~130 bits, from crypto/rand.Text.
	Replica string
	Schema  string
	Options map[string]any
	// Now is the wall clock for stamps (HLC below); nil means time.Now.
	Now func() time.Time
	// MaxSkew is how far ahead of Now a received delta's stamp may be before
	// it is rejected (ErrBadStamp), unless the delta is Trusted; zero means
	// DefaultMaxSkew. Snapshots aren't bounded.
	MaxSkew time.Duration
}

// DefaultMaxSkew tolerates ordinary client clock drift.
const DefaultMaxSkew = time.Minute

// Stamps are hybrid logical clocks in one uint64: wall-clock milliseconds
// in the high bits, a counter in the low 16. A local edit is stamped
// max(clock+1, HLC(now)) and every stamp received raises the clock
// (Lamport's rule), so stamps still order causally, but a replica that was
// offline restarts from real time: its new writes beat older ones instead
// of losing every last-writer-wins until it hears from a live peer.
const hlcCounterBits = 16

// HLC is the smallest stamp for wall time t.
func HLC(t time.Time) uint64 { return uint64(t.UnixMilli()) << hlcCounterBits }

// MaxStamp is the largest stamp acceptable at wall time now: the end of the
// millisecond now+skew.
func MaxStamp(now time.Time, skew time.Duration) uint64 {
	return HLC(now.Add(skew)) | (1<<hlcCounterBits - 1)
}

// We must make this explicit and leave nothing to inherited Go defaults
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

// View is a query result as plain JSON, readable without the engine (by MCP
// tools, tests, people). Only deltas and snapshots use the binary wire format.
//
// Its encoding is a contract, not a JSON library's defaults: the mock, the
// native engine and the WASM build must write the same bytes for the same
// document (cases in engine/conformance), and the gateway passes them to a
// browser unchanged. Object keys are sorted; tombstones are omitted.
//
//		{"clock":"117605757353984000","registers":{"a":"-12","b":1.5,"c":"aGk="}}
//
//	  - clock, IntVal: decimal strings. Both are 64-bit and a JSON number holds
//	    only 2^53 — an HLC clock runs ~13x past that, so as a number it loses
//	    the counter bits it exists to carry.
//	  - FloatVal: a number, in the shortest form that round-trips, as ES6
//	    Number.toString writes it (1, not 1.0). Non-finite values cannot appear:
//	    checkRegister rejects them, so they enter no document.
//	  - StrVal: a string. BytesVal: base64, standard alphabet, padded.
//	  - AssetRef: {"assetId":string,"version":number}
type View struct {
	Data json.RawMessage
}

type WireFormat string

const (
	WireNative WireFormat = "native" // flatbuffers, schema in engine/schema
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
	// Trusted skips the MaxSkew bound on the delta's stamp. Set it only for
	// deltas a trusted server stamped or already checked (another documents
	// replica); a browser's must be bounded where it enters.
	Trusted bool
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

// AssetFilter selects assets by ID; no IDs matches every asset.
type AssetFilter struct {
	IDs []string
}

// AssetRef is a pinned reference to a storage asset version, as the document
// holds it. Everything else about the asset (object key, type, size) is
// storage's: resolve it there (ResolveRef).
type AssetRef struct {
	ID      string
	Version int
}
