# Plan: documents (`services/documents`)

Not started. The README describes what the service does; this plan covers all of it, in
four phases. Related: [storage/README.md](../storage/README.md) (asset refs, usage,
forking, snapshot calls), [iam/PLAN.md](../iam/PLAN.md) (cloning),
[gateway/PLAN.md](../gateway/PLAN.md) (auth),
[platform/nats](../../platform/nats/README.md) (the WAL).

## Decisions (2026-10-12)

- Built on `internal/engine` through an `engine.Engine` (`New`, `Load`; the in-memory mock until the
  native engine exists). Payloads are opaque to documents: it never branches on the
  wire format, only on `Format` to pick the ledger MIME type.
- Deltas are idempotent CRDT ops with a Lamport clock; values are last-writer-wins
  registers (phase 1 makes the mock behave this way).
- Snapshot bytes go through **presigned URLs**: storage issues them and writes the
  ledger row (`Kind=snapshot`); documents does the HTTP PUT/GET.
- **No Postgres schema.** The latest snapshot is the newest pointer on the `DOCUMENTS`
  stream (`nats.LatestSnapshot`); without one (never snapshotted, or expired by
  MaxAge), fall back to storage's newest snapshot for the project.
- **One NATS consumer for the whole service**, never one per project. Per-project reads
  use Direct Get (see the rule in [platform/nats](../../platform/nats/README.md)).
- Follows the `services/storage` layout: public surface in `documents.go`/`wiring.go`,
  logic under `internal/`, one shared suite per interface, memory fakes.
- Every `internal/` package gets a short `doc.go`: what it's for, its main types, and
  which file holds what. The service is complex enough to need the map.

## Contract with other services

### Asset references

- Documents point at assets only with `AssetRef{AssetID, Version}` (storage's
  type and proto message), always pinned to a version. "Use the latest" is resolved
  when inserting. Showing "update available" and moving a ref to a newer version is a
  deliberate document edit.
- Assets are read through storage's `ResolveRef(actor, projectID, ref)`, which
  also works while the asset is in the trash (until purged) and returns `DeletedAt` so
  the UI can mark it.

### Usage rows

- Keep storage's usage table accurate: record `(project, asset, version)` when a
  ref is added to a document, forget it when the last use is removed (both idempotent).
  Access to used assets and what a clone copies both depend on these rows.

### Cloning and forking

- Clone (iam `CloneProject` -> storage `CopyProjectFiles` -> here): copy
  the document into the clone project and rewrite every ref to its copy,
  `{UUIDv5(clone project, asset, version), 1}`. Computable locally: no mapping table.
  Must be idempotent, since the clone job can resume.
- Fork (storage `ForkAsset`): rewrite the forked ref in that one document to the
  returned ref.

## Phase 1: core, in process (no NATS, no gRPC)

- `internal/live`: the registry of open documents (README "LoadOrInit" and
  "DebouncedExecutor"). **Done (2026-10-12)**: `Registry` (`Acquire`, `Close`), `Doc`
  (`Engine`, `Update`), `Loader`/`Saver` funcs, a fake clock in `export_test.go`;
  mutation-checked, race-clean in Docker.
  - `Registry.Acquire(ctx, projectID) (*Doc, release)` loads from the snapshot store or
    inits v0. Concurrent loads of one project happen once (a per-project entry with a
    ready channel; standard library only).
  - Debounced snapshots per doc (defaults: debounce 2s, max delay 30s). A change (local
    edit or applied delta) re-arms one timer for `min(lastChange+debounce,
    dirtySince+maxDelay)`, so bursts save once and constant editing still saves. On
    fire: under the lock take the engine snapshot, the covered stream sequence and a
    change counter; outside it, store, publish the pointer, compact. Mark clean only if
    the counter hasn't moved, else re-arm. One save in flight per doc; failures stay
    dirty and retry with backoff (the deltas stay in the stream until compaction).
  - TTL eviction (default 10m), a lazy debounce: every access, applied NATS deltas
    included, only records `lastAccess`; the timer re-arms for the remainder if
    `now-lastAccess < ttl`. Never evicts a doc in use (refcounted by
    `Acquire`/release). Eviction saves unsaved changes immediately (bypassing the
    debounce), and only after that succeeds closes the engine doc and drops it;
    a failed save keeps it in memory and retries.
  - Shutdown saves every dirty doc the same way, within the shutdown timeout.
  - An injectable clock (`export_test.go`, like storage's `SetClock`), so tests can fire
    the debounce and the TTL deterministically.
- `internal/snapshots`: a `Store` interface (`Latest`, `Put`, `Get`), a memory fake, and
  a shared suite (`snapshotstest.RunSuite`). The real one over storage comes in phase 3.
  **Done (2026-10-12)**: `Ref{ProjectID, ObjectID, Version, Seq}` (Version counts a
  project's snapshots from 1; Seq is the stream sequence covered), `ErrNotFound`,
  `ErrInvalid`, `ContentType` (format -> ledger MIME, version as a parameter), and
  `Memory`; mutation-checked. Storage's calls must keep the suite's contract: per-project
  versions, format and version round-tripped, and `Get` refusing a ref whose project
  doesn't own the object.
- `internal/manager`: `Manager` implements `Service`. It depends only on interfaces:
  `engine.Engine`, `snapshots.Store`, an `Authorizer` (`CanRead`/`CanWrite` on a
  project; iam via an adapter, see "Authorization: iam"), and `AssetUses` (storage's
  `RecordAssetUse`/`ForgetAssetUse`).
- `Service` (`internal/manager/service.go`, re-exported from `documents.go`):
  - `GetDocument(ctx, actor, projectID) (Document, error)`: the snapshot with the
    stream sequence it covers (0 before phase 2), so a joining client knows where the
    gateway's delta fan-out picks up
  - `QueryDocument(ctx, actor, projectID, engine.Query) (*engine.View, error)`
  - `InitDocument(ctx, actor, projectID) (engine.Snapshot, error)`: `ErrExists` if a
    snapshot already exists
  - `UpdateDocument(ctx, actor, projectID, engine.DeltaInput) (engine.Delta, error)`
  - `CloneDocument(ctx, actor, from, to string, toOwner Owner) error` (phase 4)
  - errors: `ErrNotFound`, `ErrExists`, `ErrForbidden`; `engine.ErrUnsupportedFormat`
    passes through
- Usage rows: after each edit, reconcile the document's current assets with the uses
  storage is known to hold (one sync at a time per project), not a before/after diff:
  a failed call is retried by the next edit, and concurrent edits can't reorder
  record/forget. The set starts empty after a load, so the first edit re-records
  everything (idempotent) and a failure before a restart heals. An edit applied with
  failed usage calls returns the delta together with `ErrUsesNotRecorded`.
- **`internal/manager` done (2026-10-12)**: `Manager` (`New`, `Close`), `Service`
  (`GetDocument`, `QueryDocument`, `InitDocument`, `UpdateDocument`; `Document{Snapshot,
  Seq}`), `Authorizer` + `ErrForbidden` + `AllowAll`, `AssetUses`, `ErrExists`. Loading
  creates a missing document and stores it as version 1 (README "LoadOrInit");
  `InitDocument` needs write access. 9 tests, mutation-checked, race-clean.
  `CloneDocument` is phase 4.
- Engine API additions: **done (2026-10-12)**, see `internal/engine/README.md`. `AssetRef{ID, Version}`, `ReplaceAssetRefs`, `Settings.Replica`, and a mock that is a real LWW-register CRDT with a Lamport clock, speaking the flatbuffers wire format from `engine/schema` (`MockDelta` builds test deltas). The real engine must keep the same properties; documents relies on them, not on the format.
- Tests: `manager` on memory fakes (init twice, get before init, update then get,
  forbidden paths, usage rows recorded and forgotten); `live` (debounce and max-delay
  firing, TTL eviction saving unsaved changes, concurrent `Acquire` loading once).
  Mutation-check the suites.

## Phase 2: NATS write-ahead log (done 2026-10-12)

`internal/wal` (the NATS adapter) plus the manager's use of it; `Manager.Config.WAL`
is optional (nil in unit tests). Tests of everything NATS are gated on
`MODULITH_TEST_NATS_URL`; they pass against the compose cluster and under `-race`.

- **Publishing:** `UpdateDocument` publishes the stamped delta to `document.{p}.delta`
  with headers `Modulith-Format`, `Modulith-Version`, `Modulith-Actor` and `Modulith-Origin` (this
  replica). `Nats-Msg-Id` is a UUIDv7 per publish, so a retry with the same ID is
  stored once; dedupe only saves work, since applying twice is harmless. A failed
  publish returns the delta with `ErrNotPublished`.
- **One consumer:** `wal.Log.Consume` runs one durable pull consumer over every
  project, with `Manager.ApplyDelta` as the handler:
  - a delta for a project not in memory loads it, so every project with deltas is
    snapshotted before MaxAge (7d)
  - deltas from the gateway (`Modulith-Actor`, foreign origin) sync asset uses; this
    replica's echoes were synced when made
  - an engine refusal (malformed, wrong format, forged stamp) is `wal.ErrBadDelta`:
    terminated, not redelivered. Eviction races are redelivered.
- **Loading:** from storage's latest snapshot (its `Ref.Seq` is the covered sequence,
  so the NATS pointer isn't needed here; it's for clients), then `DeltasAfter` by
  Direct Get.
- **A document's sequence advances only through the consumer**, never at publish:
  other publishers' lower-sequence deltas may not be applied yet, and claiming to cover
  them would let compaction purge them. A replica's own edits count as covered once
  their echoes come back.
- **Saving (fault tolerance):** `Put`, then publish the pointer, then `Compact`, and
  `Compact` only if `Store.Durable()`. So deltas are purged only once a snapshot
  covering them is in S3 (through storage), never because of RAM state. Deltas plus
  the durable snapshots always rebuild the latest state after a crash (tests:
  `TestNATSReplayAfterCrash`, `TestNATSNoCompactionWithoutDurableStore`).
  `snapshots.Memory` isn't durable; `NewDurableMemory` pretends to be, for tests only.
- **Never back in time:** the store refuses a `Put` covering an earlier stream sequence
  than its latest (`snapshots.ErrStale`). The saver then merges the newer snapshot into
  the open document (CRDT merge, nothing lost), raises the document's sequence and
  stores the merge (`TestSaveNeverGoesBackInTime`). This uses stream sequences
  (assigned by NATS, one total order), not Lamport clocks, which can tie or regress
  across replicas.
- **Clocks (engine):** stamps are hybrid logical clocks (`engine.HLC`: wall-clock
  milliseconds << 16, plus a counter), so an intermittent peer restarts from real time
  instead of losing every LWW until it hears from a live peer
  (`TestMockIntermittentPeerIsNotLeftBehind`). Received stamps past now + `MaxSkew` (1
  minute) are refused with `engine.ErrBadStamp`, for deltas and snapshot registers, so
  a forged stamp can't win every write or drag clocks forward
  (`TestMockRejectsFutureStamps`, `TestForgedStampsAreRefused`).

Still to do in the gateway (see gateway/PLAN.md "NATS to WebSocket"):

- bind each socket to a replica ID the gateway assigns, and refuse browser deltas
  stamped with another replica's ID (forged tie-breaks, or impersonating the server).
  Needs an engine helper to read a delta's stamp (`engine.StampOf`, to add)
- check `CanWrite`, then publish with `Modulith-Actor` set from the session
- send the server's time on connect, so the WASM engine stamps with server-adjusted
  time: a browser clock more than `MaxSkew` ahead would have its edits refused
- only the server enforces `MaxSkew`. A browser whose clock is behind must still
  accept honest deltas from the stream (they were checked before entering it), so the
  WASM engine runs without the bound

## Authorization: iam (decided 2026-10-12)

Every `Service` method checks the actor against the project before touching the
document: `GetDocument`/`QueryDocument` need read, `InitDocument`/`UpdateDocument`
need write. Phase 1 tests this with a fake `Authorizer` (readers, writers, strangers).
`manager.AllowAll` is a stand-in for tests and local experiments only: no `cmd/`
binary may wire it.

- **No new iam API.** iam's storage authorizer already answers project questions:
  `CanRead`/`CanWrite(actor, Owner{Kind: project, ID: p}, p)` resolves to the actor's
  access on project `p` (owner, org member, or share: viewer means read, editor means
  write). It refuses unknown actors, users being deleted, unknown projects, and
  projects that aren't ready (cloning) or are being deleted.
- **Adapter (done 2026-10-12):** `documents.IAMAuthorizer` (`iam_authz.go`) wraps a
  `storage.Authorizer`, calls it with the project as both owner and project, and
  re-wraps `storage.ErrForbidden` as `documents.ErrForbidden`. Other errors pass
  through unchanged (a store failure isn't "forbidden").
  - monolith (`cmd/modulith`): `iam.NewStorageAuthorizer(iamStore)`, the same value
    storage gets
  - distributed (`cmd/documents`): `iam.NewAuthzClient(conn)` to the iam service
- **Test (done):** `iam_authz_test.go` runs the adapter over iam's real
  `StorageAuthorizer` and memory store (mutation-checked). It covers:
  - a stranger is refused both read and write
  - a viewer share can read but not write
  - an editor share, the owner and an org member can write
  - a cloning or deleted project is refused
- **Phase 4 caveat:** while iam clones, the target project is `ProjectCloning`, so
  every actor check on it fails. `CloneDocument` is service to service (iam calls it,
  never the gateway): it checks read on the source for the actor, and trusts iam for
  the target, like storage's `CopyProjectFiles`.
- The gateway never exposes `actor_id`: it comes from the SuperTokens session
  (gateway/PLAN.md), so these checks see the real user.

## Phase 3: storage snapshot calls and gRPC

- Storage (a step in its own plan): `BeginSnapshot(actor, projectID) -> {objectID,
  putURL}`, `CompleteSnapshot`, `SnapshotURL(objectID)` and `LatestSnapshot(projectID)`,
  all service to service. Storage builds `projects/{id}/snapshot/{id}` and records the
  ledger row. Update storage's README ("document snapshots") and `storage.proto`.
- `internal/snapshots/storage.go`: the real `Store` over those calls and `net/http`,
  run through the shared suite against the S3 test env.
  **Done (2026-10-12):** `StorageStore` with `HTTPTransfer`; the suite passes against
  `storage.NewMemory` and against storage on Postgres + RustFS (`TestStorageStoreOnS3`).
  Snapshots carry the last edit (`live.Doc.Update` takes the actor; the saver forwards
  it; the NATS pointer has `editedBy`/`editedAt`). `Ref.ContentType` gives `Get` the
  format back. Replay now skips messages it can't decode (one bad message made a
  project unloadable).
  - **storage side done (2026-10-12)**: see storage's README "Document snapshots".
  - `Put` returns only once the object is confirmed in S3 (`CompleteSnapshot` checks
    it), never when the URL is issued, and storage checks staleness and records the
    row atomically (`ErrStale` as one transaction). `Durable()` is true.
- `contracts/proto/modulith/documents/v1/documents.proto`: `DocumentsService` mirroring
  `Service`; snapshots and deltas as `bytes` with format and version.
- `internal/grpcapi`: `Server`, a `Client` implementing `Service`, a sentinel-error
  table (`wireErrors` style), and a bufconn test running the manager suite.
- Wiring: `documents.go` (`RegisterGRPC`, `NewGRPCClient`), `wiring.go` (settings:
  debounce, max delay, TTL; `Open`), `cmd/documents`, and `cmd/modulith`. No migrations,
  so no `Migrate`.
- **Step 4 done (2026-10-12):** `DocumentsService` (Get, Query, Init, Update), `grpcapi`
  (one error table; an applied edit that wasn't published or whose uses failed still
  returns its delta, the problem in the response), `SettingsFromEnv`, `Open`,
  `StartConsumer`, `cmd/documents` (port 9094), the monolith (NATS, documents, its
  consumer; shutdown saves open documents before NATS and the pool), the Docker target
  and the distributed compose service. Smoke-tested end to end on the monolith (iam,
  documents, NATS echo, debounced save to RustFS with the editor recorded). Found on
  the way: deltas for unknown or deleted projects were redelivered forever (now
  dropped, `snapshots.ErrNoProject`), and `.dockerignore` excluded `apps/admin`, so
  no image had built since the admin app.

## Phase 4: cloning and forking

- `CloneDocument`: load the source snapshot, `Merge` it into a new engine doc,
  `ReplaceAssetRefs` every ref with `{storage.CopyAssetID(to, toOwner, ref), 1}`,
  `RecordAssetUse` the new refs, and store v0 for `to`. Idempotent: if `to` already has
  a snapshot, succeed without doing anything.
- iam: step 3 of `manager_clone.go` calls `documents.Service.CloneDocument` (a new iam
  dependency, injected like storage; update iam's README and PLAN).
- Fork: after storage's `ForkAsset` returns the new ref, rewrite that one ref
  (`ReplaceAssetRefs` with one entry) through the `UpdateDocument` path, so it's
  published as a delta.

## Open

- A whitelisted relay of frontend user-state subjects (README NATS section). The
  gateway subscribes to `userstate.>` itself for now.
- Load management and replication across replicas (README: a router and a table
  assigning projects to replicas). Several replicas sharing the one durable consumer
  would split the deltas between them, so replicas need per-replica consumers
  filtered to their assigned projects (still per replica, not per project).
- Whether browsers connect to replicas directly; if so, replicas verify SuperTokens
  access tokens themselves (see gateway/PLAN.md).
- The real engine's wire formats (flatbuffers) replacing the mock's JSON.

## Reverting (done 2026-10-12)

Administrative un-sticking: put a project back to an earlier stored version. Not undo.

- **A revert is an edit**, never "make an old snapshot latest". With CRDTs, any replica
  or browser holding newer state would merge it back (newer stamps win). Instead the
  engine diffs the current state against the target, sets every changed key back and
  tombstones keys the target didn't have, all stamped with a fresh tick. That delta
  wins last-writer-wins everywhere and is published and saved like any edit. So there's
  no reset protocol, the revert is in history (and revertible), and offline peers'
  older stamps lose.
- engine: `Document.RevertTo(ctx, snapshot) (Delta, error)`. Mock now, contract for
  the native engine.
- storage: `ListSnapshots` (newest first) and `SnapshotURL`. Retention
  (`MODULITH_SNAPSHOT_KEEP`, 100) is the revert window.
- documents: `RevertDocument(ctx, actorID, projectID, version)`: fetch that version,
  `RevertTo`, then publish, sync asset uses and save as usual.
- **Built:** `engine.Document.RevertTo`, `RevertDocument(ctx, actorID, projectID,
  version)` on the Service and over gRPC (`snapshots.Store.Version` finds the version;
  storage needs nothing new), and the admin project page (live document, snapshot list,
  revert buttons). Smoke-tested in the monolith: the revert is one published edit,
  saved as the next version, and a stranger is refused.
- **Who may revert:** anyone with write access, for now. The admin tool acts as any
  user, so it works today; once iam has a staff flag, the gateway won't route it and
  documents checks the flag.
