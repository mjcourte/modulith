# NATS/JetStream

- Streams are created or updated at startup by `EnsureStreams` (called from `Connect`), like migrations.
- Storage per member is 100MB (`nats-*.conf`): this is a dev toy cluster, and messages are small (snapshots travel as pointers, not bytes).

## Go package

`gyoza/platform/nats`: `client.go` (`Connect`, `Close`), `streams.go` (stream configs, `EnsureStreams`, `PurgeDeltas`), `subjects.go` (subject patterns and builders). The client library is imported as `natsgo`, so it doesn't read as this package.

Always build subjects with the builders (`DocumentDelta(projectID)`, ...): they reject IDs that are empty or contain `.`, `*`, `>` or whitespace, which would otherwise add a subject level or act as a wildcard.

## Topology: one code path

Monolith and distributed mode both connect to an external cluster through `GYOZA_NATS_URL` (the compose file runs three members; host clients reach `nats-1` on `localhost:4222`). Even when gateway and documentmanager share one process they talk over NATS, because several instances need the fan-out anyway. No binary connects yet: the client is wired into `cmd/` once gateway or documentmanager has a consumer.

## Document WAL design

Deltas are published to `document.{projectID}.delta`. Every replica (documentmanager, each frontend through gateway) applies them. documentmanager is the only publisher of `document.{projectID}.snapshot` and never consumes it. After it stores a snapshot in S3 (through storage), it publishes a pointer to it and purges the deltas the snapshot covers.

| Component           | Responsibility                                                                 | Durability                |
| ------------------- | ------------------------------------------------------------------------------ | ------------------------- |
| JetStream deltas    | Buffer and deliver new deltas to every consumer                                | Until purged (MaxAge 7d)  |
| Document manager    | Apply deltas, maintain live state, snapshot to S3, announce it, purge deltas   | In-memory replica         |
| JetStream snapshots | Latest snapshot pointer per project                                            | Latest per subject        |
| RustFS/S3           | Store the snapshots (versioned)                                                | Durable                   |
| Gateway->gRPC       | Request current document state from the manager                                | Request/response lifetime |
| FE clients          | Apply deltas; on a snapshot pointer, fetch it and merge into the live replica ([apps/web/PLAN.md](../../apps/web/PLAN.md)) | In-memory replica |

## Streams

|           | DOCUMENT_DELTAS                                                     | DOCUMENT_SNAPSHOTS                                                    |
| --------- | ------------------------------------------------------------------- | --------------------------------------------------------------------- |
| Subjects  | `document.*.delta`                                                  | `document.*.snapshot`                                                 |
| Purpose   | Write-ahead log delivered to every consumer                         | Latest snapshot pointer: version, last delta sequence covered, object ID |
| Retention | Limits (not work queue: that allows one consumer per subject)       | Limits, `MaxMsgsPerSubject: 1`                                        |
| Removal   | `PurgeDeltas` after the covering snapshot is in S3; MaxAge 7d backstop | Replaced by the next snapshot                                      |
| Limits    | 256KB per message, 2m dedupe window on `Nats-Msg-Id`                | 4KB per message                                                       |
| Replicas  | 3, file storage                                                     | 3, file storage                                                       |

Planned consumers (not built): documentmanager reads deltas with a durable pull consumer. Gateway opens an ordered consumer per open project: the last snapshot (`DeliverLastPerSubject`), then deltas from that snapshot's sequence.

## User state (core NATS, no stream)

`userstate.{projectID}.{userID}.viewing` and `.cursorpos` are plain pub/sub: they change constantly and go stale in seconds, so nothing is persisted. "Viewing" is a heartbeat that subscribers expire when it stops. Gateway subscribes to `UserStateAll(projectID)` (`userstate.{projectID}.>`).

## Tests

Subject tests always run. The stream tests need the cluster (`Replicas: 3`):

```sh
docker compose -f deploy/monolith/compose.yaml up -d --wait nats-1 nats-2 nats-3
GYOZA_TEST_NATS_URL=nats://localhost:4222 go test ./platform/nats -v
```
