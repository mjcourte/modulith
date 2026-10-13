# NATS/JetStream

- Streams are created or updated at startup by `EnsureStreams` (called from `Connect`), like migrations.
- Storage per member is 100MB (`nats-*.conf`): this is a dev toy cluster, and messages are small (snapshots travel as pointers, not bytes).

## Go package

`gyoza/platform/nats`: `client.go` (`Connect`, `Close`), `streams.go` (the stream config, `EnsureStreams`, `Compact`), `documents.go` (consumer-free reads: `LatestSnapshot`, `DeltasAfter`), `subjects.go` (subject patterns and builders). The client library is imported as `natsgo`, so it doesn't read as this package.

Always build subjects with the builders (`DocumentDelta(projectID)`, ...): they reject IDs that are empty or contain `.`, `*`, `>` or whitespace, which would otherwise add a subject level or act as a wildcard.

## Rule: few consumers

Consumers are per service instance, never per project, per client or per request. Per-project reads (the latest snapshot pointer, catching up on deltas) use Direct Get, and fan-out to clients happens inside a service, after its one consumer. (The previous version of Gyoza scaled to about 3N consumers per active project, which is not how NATS is meant to be used.)

## Topology: one code path

Monolith and distributed mode both connect to an external cluster through `GYOZA_NATS_URL` (the compose file runs three members; host clients reach `nats-1` on `localhost:4222`). Even when gateway and documents share one process they talk over NATS, because several instances need the fan-out anyway. `cmd/gyoza` and `cmd/documents` connect (documents' consumer); gateway will too.

## Document WAL design

Deltas are published to `document.{projectID}.delta`. Every replica (documents, each frontend through gateway) applies them; applying is idempotent (CRDT ops with a Lamport clock), so redeliveries and echoes are harmless. documents is the only publisher of `document.{projectID}.snapshot`. After it stores a snapshot in S3 (through storage), it publishes a pointer to it, then `Compact`s: the deltas the snapshot covers and the older pointers are purged.

| Component           | Responsibility                                                                 | Durability                |
| ------------------- | ------------------------------------------------------------------------------ | ------------------------- |
| `DOCUMENTS` stream  | Deltas and snapshot pointers for every project                                 | Until compacted (MaxAge 7d) |
| Documents service   | One consumer: apply deltas, maintain live state, snapshot to S3, announce it, compact | In-memory replica  |
| RustFS/S3           | Store the snapshots (versioned)                                                | Durable                   |
| Gateway             | One consumer per instance, fanned out to the WebSockets of each project; joining clients get state from documents (`GetDocument`) | None |
| FE clients          | Apply deltas; on a snapshot pointer, fetch it and merge into the live replica ([apps/web/PLAN.md](../../apps/web/PLAN.md)) | In-memory replica |

## Stream `DOCUMENTS`

| | |
| --- | --- |
| Subjects  | `document.*.delta`, `document.*.snapshot` |
| Purpose   | Write-ahead log (deltas) and the latest snapshot pointer per project: version, last delta sequence covered, object ID |
| Retention | Limits (not work queue: that allows one consumer per subject). No `MaxMsgsPerSubject`: it would cap deltas too |
| Removal   | `Compact` after the covering snapshot is in S3 and its pointer published; MaxAge 7d backstop |
| Reads     | `AllowDirect`: `LatestSnapshot` and `DeltasAfter` use Direct Get, no consumer |
| Limits    | 256KB per message, 2m dedupe window on `Nats-Msg-Id` |
| Replicas  | 3, file storage |

MaxAge can expire an idle project's last pointer; documents then falls back to storage's latest snapshot for the project.

`DeltasAfter` makes one Direct Get per delta (nats.go has no batch Direct Get helper). Snapshots keep the tail short; batch Direct Get is the upgrade if replays get long (`OPTIMIZATIONS.md`).

Planned consumers (not built): documents, one durable pull consumer on `document.*.delta`; a delta for a project not in memory loads it (`LatestSnapshot`, then `DeltasAfter`). Gateway, one ordered consumer per instance on `document.>` from new messages, routed by project to open sockets.

## User state (core NATS, no stream)

`userstate.{projectID}.{userID}.viewing` and `.cursorpos` are plain pub/sub: they change constantly and go stale in seconds, so nothing is persisted. "Viewing" is a heartbeat that subscribers expire when it stops. Each gateway instance subscribes once to `userstate.>` and fans out by project, like the stream (`UserStateAll(projectID)` builds one project's filter).

## Tests

Subject tests always run. The stream tests need the cluster (`Replicas: 3`):

```sh
docker compose -f deploy/monolith/compose.yaml up -d --wait nats-1 nats-2 nats-3
GYOZA_TEST_NATS_URL=nats://localhost:4222 go test ./platform/nats -v
```
