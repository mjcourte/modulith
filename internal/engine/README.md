# Engine

This folder contains the engine ffi for Go.

## working mock GO API

This describes mostly how the backend would interact with engine. I'm sure there's missing details for FE.
The engine itself should be C++ or Rust

```go
// ctor
document := Document.New(document_settings) // make a new document with settings. what will it know from IAM?

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
