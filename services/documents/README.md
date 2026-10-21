# Documents

- transacts/log rolls snapshots to RustFS/S3
- recieves deltas from any entry point, and uses engine go api to update
- never touches S3 directly: snapshot reads and writes go through the storage service over gRPC (`StorageService` snapshot calls, see the storage README "Document snapshots"; the bytes go to S3 by presigned URL)
- snapshot convention (agreed with storage): storage records every snapshot in its ledger with `Kind=snapshot`, a vendor MIME type (e.g. `application/vnd.modulith.app+flatbuffers`), and an S3 key under `projects/{projectID}/snapshot/{id}` (see the storage README "S3 key layout"). Snapshots are not DAM assets

## Status (2026-10-12)

Built and running in `cmd/modulith` and `cmd/documents` (gRPC `DocumentsService`, port 9094 standalone). Layout, one `doc.go` per package:

| Path | What |
| --- | --- |
| `documents.go`, `wiring.go` | public surface; `SettingsFromEnv`, `Open`, `StartConsumer`; `IAMAuthorizer` (`iam_authz.go`) |
| `internal/live` | open documents: load once, debounced saves, TTL eviction |
| `internal/manager` | `Service`: Get, Query, Init, Update, Revert, Clone, ReplaceAssetRef; `ApplyDelta` for the consumer; asset uses |
| `internal/snapshots` | `Store`: `Memory` (tests), `StorageStore` (S3 through storage, durable) |
| `internal/wal` | the NATS write-ahead log: publish, replay by Direct Get, the one consumer |
| `internal/grpcapi` | `DocumentsService` server and client |

The engine is `internal/engine`'s mock until the native one exists. Decisions and what's next: PLAN.md.

## Internal Methods

### LoadOrInit

loadOrInit: a pre-check for read/write ops:

- Is the project in memory? if yes, move on
- If NO, load if from S3
- Is it IN S3?? if yes load it
- If NO, init, push to S3 v0, carry on
- Should also update NATS snapshots stream with latest

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
- storage records the snapshot in its ledger

### CloneDocument

- Init to new ID with actor as owner
- Calls to/from(?) IAM as needed for this
- Calls to/from Storage service re: asset cascades

### UpdateDocument

- The general write entrypoint
- Accepts inputs from all entry points: local call, NATS, gRPC, REST
- runs the engine document edit/write API

## NATS

Streams and subjects are defined in `platform/nats` (see its README).

- Both subjects are in the one `DOCUMENTS` stream. The service has one consumer for all projects (durable pull, `document.*.delta`) and publishes deltas too; per-project reads (the latest pointer, catching up on load) use Direct Get, never a consumer.
- The only publisher of `document.{projectID}.snapshot`. The message is a pointer (version, last delta sequence covered, storage object ID), not the snapshot bytes.
- After a snapshot is stored in S3 through storage: publish the pointer, then `Compact(projectID, coveredSeq)`. Order matters: compacting first would lose deltas if the publish fails.
- Whitelist based filter to allow FE P2P NATS usage eg userstate cursor etc. (userstate is core NATS, not JetStream)

## Load management and replication

As a monolith this is basically impossible so lets leave this to a hypothetical never future.
Basically need a router/load balancer service and eg PG table that assigns project(s) to documents(s),
and routes whatever gRPC calls..
