// Package wal is the documents service's side of the NATS write-ahead log
// (platform/nats, stream DOCUMENTS).
//
//   - wal.go: Log (NATS-backed), Delta (a delta message and its headers),
//     Pointer (the snapshot pointer clients read).
//   - Publishing: PublishDelta (an edit; its message ID is a UUIDv7, so a retry
//     with the same ID is stored once), PublishSnapshot then Compact after a
//     snapshot is stored.
//   - Reading: DeltasAfter replays one project's deltas by Direct Get (on
//     load); Consume runs the service's one durable consumer over every
//     project's deltas.
//
// Payloads stay opaque: the wire format and version travel in headers, so
// nothing here parses a delta.
package wal
