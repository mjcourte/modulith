# Engine

This folder contains the engine ffi for Go.

## Status

The API compiles and has an in-memory mock:

- `Document` (`document.go`) is an interface. Callers take an `Engine` (`New(ctx, Settings)` for an empty document, `Load(ctx, Settings, Snapshot)` to open a stored one), so the monolith can inject `engine.Mock{}` today and the native binding later (a separate file behind a cgo build tag) without changing documents or gateway.
- `Mock` (`mock.go`) is a small real CRDT. Its `Load` merges into an empty document (the native engine can load directly; `Merge` stays for folding another replica's state into a live document): a map of last-writer-wins registers, each stamped `(lamport, replica)`, the higher stamp winning, ties going to the higher replica ID, then the greater value. Applying a delta or merging a snapshot is idempotent and order-independent, so redelivered, reordered or replayed deltas are harmless, and replicas that saw the same deltas produce byte-identical snapshots (registers are written sorted). Deletes are tombstones, so an older write can't bring a key back.
  - `Edit` stamps an unstamped delta (lamport 0) with this replica's next tick (`Settings.Replica`, random if empty) and returns it stamped; a received stamp raises the clock (Lamport's rule).
  - Stamps are hybrid logical clocks (`HLC`: wall-clock ms << 16, plus a counter): a tick is `max(clock+1, HLC(now))`, so a peer back from offline stamps from real time rather than far below the live peers. `Settings.Now` injects the wall clock.
  - Received deltas stamped past now + `Settings.MaxSkew` (default 1 minute) are refused with `ErrBadStamp`, so a forged stamp can't win every write or drag clocks forward. `DeltaInput.Trusted` skips the bound, for deltas from servers, whose clocks may run ahead; snapshots aren't bounded. Only servers should enforce it. Failure modes and decisions: [PLAN.md](PLAN.md).
  - Equal stamps (two writers sharing a replica ID) are broken by the value, so replicas still converge.
  - Any register holding an `AssetRef` is a use of that asset version: `Assets` lists the distinct ones, and `ReplaceAssetRefs` rewrites them as one local edit (an empty `Payload` if nothing used them).
  - `RevertTo(snapshot)` diffs the live registers against a snapshot's and writes the difference (changed keys set back, newer keys deleted) as one fresh edit, so a revert wins everywhere and is itself in history. Empty delta if nothing differs.
  - `Query` ignores the query and returns the live registers as plain JSON (`{"clock": n, "registers": {key: value}}`). `RenderImage` returns a blank PNG of the requested size; `RenderVideo` returns `ErrUnsupportedFormat`.
- Wire format (`mock_wire.go`): deltas and snapshots are flatbuffers (`WireNative`, version 1) from the schema in [`engine/schema`](../../engine/schema), which the native engine and the frontend's WASM build share. Values are a small union: int, float, string, bytes, `AssetRef`. Buffers are checked by file identifier (`GYDL` delta, `GYSN` snapshot); the Go runtime has no verifier, so a malformed buffer's panic becomes an error. `View` stays JSON so it can be read without the engine.
- `MockDelta(replica, lamport, sets...)` builds deltas for tests and tools (documents, gateway) without hand-written flatbuffers.
- Generated Go is committed in `fbs/`; after editing the schema run `go generate ./internal/engine` (needs `flatc`, from MSYS2: `pacman -S mingw-w64-ucrt-x86_64-flatbuffers`).
- Errors: `ErrClosed` (any call after `Close`), `ErrUnsupportedFormat` (wrong format, version or buffer type), `ErrNotFound` (an asset ID the document doesn't use).

## Original API sketch

This describes mostly how the backend would interact with engine. I'm sure there's missing details for FE.
The engine itself should be C++ or Rust

```go
// ctor
document := Document.New(document_settings) // make a new document with settings. what will it know from IAM?
document := Document.Load(snapshot)
// get views as typed objects
view := document.Query(query) // query can take a few forms includign graphQL, and shall be normalized behind this facade

// CRDT operations
document.Merge(snapshot_wire_fmt) // merges two documents, updating self in-place
delta_wire_fmt := document.Edit(delta_any_fmt) // applies a delta (of any allowable format, to be normalized behind facade) to the document, updating self in-place, delta is returned for NATS distribution
snapshot_wire_fmt = document.Snapshot() // dump/export live representation to wire format

// Rendering ops
image_bytes := document.RenderImage(query, format) // produces bytes of image based on the result of query, with imageprops as per format
video_byes := document.RenderVideo(query, format) // same, but video. query will include scene contents, time, time range, etc.

// Asset management
document.LoadAssets(filter) // pull assets from RustFS/S3 store to memory, all or filtered. via gateway->storage service (FE) or storage service (BE)
document.UnloadAssets(filter) // dump assets from memory, all or filtered
// Asset registration is like querying the document for an asset index which should be maintained as individual ECS entities reference assets
```
