# Document Manager

- Rust (native to whatever the rendering engine is) or actually attempt to redo this in Go? Probably best, for monolithing, so long as we get clean engine interop.
- basically Kyoso-server
- transacts/log rolls snapshots in S3
- server side rendering NOPE this is split to services/exporter
- does load balancing NOPE just forget all that for now lol
- Document puts to S3 might want to be recorded in the ledger (the storage service's `LedgerService`), so the files are in pgSQL ledger
- snapshot convention (agreed with storagemanager/storageledger): record every snapshot in the ledger with `Kind=snapshot`, a vendor MIME type (e.g. `application/vnd.gyoza.app+flatbuffers`), and an S3 key under `projects/{projectID}/snapshot/{id}` (see storagemanager README "S3 key layout"). Snapshots are not DAM assets

## Internal Methods

### LoadOrInit

loadOrInit: a pre-check for read/write ops:

- Is the project in memory? if yes, move on
- If NO, load if from S3
- Is it IN S3?? if yes load it
- If NO, init, push to S3 v0, carry on

### DebouncedExecutor

Memory manager for projects.
We want to debounce snapshot->S3 project on-change with a maximum debounce.
We want to also TTL evict projects from memory (back to S3) with TTL extension on-event.

## Service Methods

I'm going to say S3 a bunch, but i mean S3 or RustFS.

### GetDocument

- checks if doc already in RAM or pulls document snapshot from S3
- check NATS jetstream for outstanding messages on this subject, process them if so, to bring local mem state up to latest
- produce wire format (fbs?) for serving over whatever method (monolith function call, NATS, REST, gRPC)
- Must loadOrInit

### QueryDocument

- graphQL method to get specific properties, eg for MCP tooling
- Must loadOrInit

### InitDocument

- precheck if exists in "S3"
- create a new engine doc, put v0 snapshot to S3, probably also get/return to caller.
- Storage Ledger records snapshot's existence

### CloneDocument

- Init to new ID with actor as owner
- Calls to/from(?) IAM as needed for this
- Calls to/from Storage service re: asset cascades

### UpdateDocument

- The general write entrypoint
- Accepts inputs from all entry points: local call, NATS, gRPC, REST
- runs the engine document edit/write API

## NATS

- Need a subscriber to projects.projectid.delta and projects.projectId.snapshot
- Should publish to both, but never recieve snapshots, only publish!
- Whitelist based filter to allow FE P2P NATS usage eg userstate cursor etc.
- Needs to actively manage the Jetstream, to consume/delete messages once existing in S3

## Load management and replication

As a monolith this is basically impossible so lets leave this to a hypothetical never future.
Basically need a router/load balancer service and eg PG table that assigns project(s) to documentmanager(s),
and routes whatever gRPC calls..
