# Engine

- ez just reproduce the entire
- transport schema eg flatbuffers wire format
- CRDT layer -> Yrs?
- C++ or Rust graph representation, i guess we'd call this "native"?
- document API
- ECS layer? do we need ECS?
- rendering layer
- effects n shaders
- animation and playback layers
- no problem bro

This folder contains the actual source code C++ or Rust w/e

## Wire schema

`schema/` holds the flatbuffers schema for deltas and snapshots (`common.fbs`, `delta.fbs`, `snapshot.fbs`). It's the contract between the engine (Rust), the frontend (the same engine as WASM) and Go (`internal/engine`, generated with `go generate ./internal/engine`). Documents relies on its CRDT properties, not its layout: last-writer-wins registers stamped `(lamport, replica)`, tombstones for deletes, idempotent and order-independent application.
