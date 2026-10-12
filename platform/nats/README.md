# NATS/JetStream

- We will use unlimited retention/service managed
- Initially setting the storage per member to 100MB, trivially small basically for two reasons:
  1. this is dev toy monolith
  2. messages are small anyways

## Go package

`gyoza/platform/nats` (`client.go`, `streams.go`, `subjects.go`): the client constructor for NATS and JetStream, the stream definitions and the subject names. The client library is imported as `natsgo`, so it doesn't read as this package.

## Document WAL design

i plan to make a WAL, where the Document.docID.delta or Document.docID.snapshot subjects are published to JetStream. FE clients (via gateway) will "get latest" Document.docID, and it will be delta OR snapshot. Document manager will maintain a local replica of document and apply incoming deltas (same as frontends). Document manager will be the only publisher of .snapshot, and it will not reconsume it. it will also save those to rustfs/S3. Once a delta is persisted to rustfs/s3, the message can be purged. likewise with snapshots, we only need to keep latest for each docID

| Component           | Responsibility                                                          | Durability                |
| ------------------- | ----------------------------------------------------------------------- | ------------------------- |
| JetStream deltas    | Buffer and deliver new deltas everywhere                                | Temporary                 |
| Document manager    | Apply deltas, maintain live state, snapshot to JetStream + S3           | In-memory replica         |
| JetStream snapshots | Distribute snapshots to live consumers                                  | Temporary w/TTL           |
| RustFS/S3           | Store the latest durable snapshot per document (versioned)              | Durable                   |
| Gateway->gRPC       | Requuest current document state from the manager                        | Request/response lifetime |
| FE Clients          | REST Request snapshot on load, Send/Recieve stream deltas and snapshots | In-memory replica         |

## Main Document subjects

|           | DOCUMENT_DELTAS                     | DOCUMENT_SNAPSHOTS                                                  |
| --------- | ----------------------------------- | ------------------------------------------------------------------- |
| Subjects  | `document._.delta`                  | `document._.snapshot`                                               |
| Purpose   | Temporary durable WAL delivery      | Latest snapshot available to consumers                              |
| Retention | Work queue                          | Limits                                                              |
| Replicas  | 3                                   | 3                                                                   |
| Storage   | File                                | File                                                                |
| Removal   | After successful processing and ACK | Keep latest message per document subject (ttl, can always REST GET) |
