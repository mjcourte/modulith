# Document Manager

Formerly "document server"; renamed to match the service naming scheme and avoid the overloaded word "server".

- Rust (native to whatever the rendering engine is)
- or actually attempt to redo this in Go?
- basically Kyoso-server
- transacts/log rolls snapshots in S3
- server side rendering
- does load balancing
- Document puts to S3 might want to be recorded in the ledger (the storage service's `LedgerService`), so the files are in pgSQL ledger
- snapshot convention (agreed with storagemanager/storageledger): record every snapshot in the ledger with `Kind=snapshot`, a vendor MIME type (e.g. `application/vnd.gyoza.app+flatbuffers`), and an S3 key under `projects/{projectID}/snapshot/{id}` (see storagemanager README "S3 key layout"). Snapshots are not DAM assets
- This was previously build on Axum. But we could look at Dioxus or Leptos for consistency

## Crates, from memory

- main service binary crate
- load balancer/router binary crate
- types/contracts crate
- reusable data structures/classes (eg limited debounce executor)
- Rust-NATS integration, as a crate (in case we swap broker stack)
- AWS SDKs managed by crates (in case we swap Postgres, Dynamo, S3 for something else)

## What do we need to build here if GoLang

Connections: REST, gRPC, NATS, monolith direct service calls

### service method GetDocument

- checks if doc already in RAM or pulls document snapshot from S3
- check NATS jetstream for outstanding messages on this subject, process them if so, to bring local mem state up to latest
- produce wire format (fbs?) for serving over whatever method (NATS, REST, gRPC, monolith)
