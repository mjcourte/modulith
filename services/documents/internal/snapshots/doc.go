// Package snapshots stores document snapshots (README "transacts/log rolls
// snapshots to RustFS/S3").
//
//   - Store (snapshots.go): Put a snapshot covering a stream sequence, get the
//     Latest one's Ref, Get its bytes. Refs number a project's snapshots
//     1, 2, 3, ... (Version), which the NATS pointer carries.
//   - Memory (memory.go): the in-memory fake (not durable), for tests.
//   - StorageStore (storage.go): the durable Store over storage's snapshot
//     calls; the bytes go straight to S3 by presigned URL (HTTPTransfer).
//   - ContentType / ParseContentType: wire format and version <-> the MIME
//     type storage records.
//   - snapshotstest: the shared suite every Store runs.
//
// The bytes are opaque here; ContentType maps the engine's wire format to
// the MIME type storage records in its ledger.
package snapshots
