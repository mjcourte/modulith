# Gateway: optimizations

Performance findings, with evidence. See `services/README.md` for the format.

## Done

- **WebSocket fan-out encodes once** (2026-10-13): each NATS message is encoded to JSON
  once and the same bytes queued to every socket in the project; projects nobody
  joined are skipped before encoding (`internal/ws/hub.go`, `fanout`).

## Open

- **Binary WebSocket frames.** Deltas and snapshots travel base64 inside JSON text frames,
  a third larger than the bytes. When to act: deltas show up in bandwidth or CPU
  profiles; then send them as binary frames with a small header.
- **One `CanWrite` call per WebSocket edit.** In-process it's a memory lookup; over gRPC
  it's a round trip to iam per edit. When to act: edit latency or iam load shows it;
  then cache the answer per socket and project for a few seconds (a revoked share then
  takes that long to apply).
- **Access re-checks poll iam** (2026-10-13): every socket calls `CanRead` once a minute
  per joined project (`socket.recheck`), a few iam queries each, whether or not
  anything changed. When to act: iam load from re-checks shows up; then have iam
  publish revocations (unshare, member removed, account deleting) on NATS and drop
  sockets on those, keeping a slow poll as the backstop.
- **Joins block the socket's later messages** while `GetDocument` loads the document
  (messages are handled in order). When to act: users notice a slow first edit after
  switching projects; then load in the background and queue that project's messages.
