# Storage

One service for every file Modulith keeps in S3: presigned uploads and downloads,
the DAM (assets, versions, folders, trash), the cleanup job, and the ledger
that records each S3 object. Other services never touch S3 or the storage
schema themselves; they call storage (in-process in the monolith, gRPC when
distributed). That includes the documents service's snapshots.

## Decisions

- uploads and downloads use presigned S3 URLs: the browser talks to S3 directly, our servers never carry file bytes
- upload is two-phase: begin (pending session + presigned PUT that signs size and content type), then complete (verify the blob, record it in the ledger)
- HTTP routes live in the gateway; this service exposes a Go `Service` and the same over gRPC (see API)
- one Postgres schema, `storage`: the ledger (`objects`) and the DAM (`folders`, `assets`, `asset_versions`, `uploads`); each asset version points at a ledger object ID
- user removal: transfer the user's objects to their org (a user has at most one), or delete them if they have none
- trash is kept 7 days, then purged for good by a cleanup job that runs in every binary hosting storage (see Cleanup and retention)

## Ledger

Every object stored in S3 has one row in `storage.objects`, whatever uses it (asset version, export, snapshot). It's the index for ownership, GDPR deletion and purging. Code: `internal/ledger` (`Service`, `Memory`, `Postgres`); the Manager is its only caller.

- every object has exactly one owner: a project, a user, an org, or Modulith (shared/common files)
- an object's lifetime follows its owner: deleting an owner deletes everything it owns
- `CreatedBy` is the user who uploaded/produced it; can differ from the owner (e.g. org assets)
- `ProjectID` is optional and means "associated with this project", whoever owns it. A project-owned object always has `ProjectID` set to its owner (enforced on create)
- a project has exactly one document (or none until the documents service initializes it), per the engine's `App`, so there is no separate document scope
- `Kind`: snapshot, asset, export (initial set). It says what **role** a stored object plays. Not to be confused with the DAM's `Category` (image, video, font, ...), which says what **kind of media** an asset is and exists only for DAM assets
- `ContentType`: the object's MIME type. Document snapshots should use a dedicated vendor type (e.g. `application/vnd.modulith.app+flatbuffers`) so a snapshot's format is known from the ledger alone
- user/org/project IDs are opaque strings owned by iam, not foreign keys
- `Create` assigns an ID, or takes one the caller chose (at most 128 bytes, printable ASCII, no spaces). With a chosen ID it is idempotent: the same file again (bucket, key, size, type, kind, project) returns the existing object; a different file is `ErrAlreadyExists`. Owner and creator are not compared, since transfers and user removal change them
- one ledger object per S3 key is an invariant the Manager keeps (not enforced by the schema: the tests reuse keys): purging an object deletes its file
- `ListDeleted(before, limit)` lists deleted objects longest-deleted first; `Purge(id)` removes a deleted object's row for good (a live one is `ErrNotDeleted`; a missing one is fine). Only the cleanup job calls them, after deleting the file
- `List`: exactly one scope (project or owner) plus optional filters (`CreatedBy`, `Kind`, `IncludeDeleted`); keyset pagination ordered by (`CreatedAt`, `ID`) with opaque page tokens. Only add a filter when something needs it; each must be supported and indexed by every implementation

## Owner lifecycle

Orchestrated by iam through `DeleteOwned` and `RemoveUser` (service to service, never exposed to browsers), which cover the DAM and the ledger together.

- **project deleted**: everything owned by the project is deleted; objects owned by others but associated with the project survive
- **user leaves with an org** (a user has at most one): everything they own moves to the org
- **user leaves without an org**: everything they own is deleted
- either way, the user is cleared as creator everywhere (including soft-deleted rows)
- **last user leaves an org**: the org's objects are deleted
- projects never transfer: `TransferOwner` refuses project owners on either side
- soft-deleted rows are hard-purged by the cleanup job once the retention period is over, after their files are deleted from S3
- a deleted project's asset uses are forgotten (`OwnerDeletion.Uses`); other projects' uses of its files are kept until the purge
- planned: iam publishes `project.deleted` on JetStream; a consumer runs the cascade, so a crash mid-cascade resumes on redelivery
- dangling references (settled 2026-10-12): a project uses in place only its own files, its owner's and modulith's (see Asset refs), so deleting an owner can't strand another owner's documents: a project's files go with it, an owner's projects go with the owner (or move to the org together with the library), and modulith's files are never deleted by users. What remains is an owner trashing a file their own projects use, which is theirs to decide; the trash shows use counts so the UI can warn

## S3 key layout

Keys only contain facts that never change and aren't personal data. S3 can't
rename (a "move" is copy + delete), so anything that can change, like the owner
or the DAM folder, stays out of the key; the ledger is the index for those.

```sh
projects/{projectID}/{kind}/{id}    objects associated with a project
library/{kind}/{id}                 objects with no project (user/org asset libraries)
```

- `kind`: the ledger `Kind` (`asset`, `export`, `snapshot`)
- `id`: an ID that makes the key unique: random for uploads, derived for copies (the copy's ledger object ID, so a retried copy writes the same key)
- never in a key: user IDs, org IDs, owner kind, folder names, file names
- a project ID must be a single plain path segment (no `/`, `\`, `.` or `..`), or the upload is refused: otherwise it could produce a key outside the layout
- why this shape: per-project browsing in the S3 console, lifecycle rules by prefix (e.g. expire `projects/*/export/`), bulk delete by project prefix if ever needed, and one prefix per project spreads S3's per-prefix request limits
- the Manager is the only code that builds keys (one function); callers never pass a key in. Other services never write to S3 themselves: they go through storage

## URL lifetimes

- `blob.Store` (`internal/blob`) takes an explicit TTL (mechanism); how long URLs last is Manager policy
- defaults: upload URLs 15 min, download URLs 5 min, overridable with `MODULITH_UPLOAD_URL_TTL` / `MODULITH_DOWNLOAD_URL_TTL`
- S3 checks expiry when a request starts, so a long upload that starts in time still completes

## IDs

- presign tokens (memory fake): `crypto/rand.Text()`, since they only need to be unguessable
- table IDs (assets, and later folders and upload sessions): UUIDv7 via `github.com/google/uuid`. They start with a timestamp, so ID order is creation order: paging orders by ID alone, and Postgres inserts land at the end of the index (cheaper than the ledger's random IDs, see OPTIMIZATIONS.md)
- ordering is guaranteed within one process (the library adds a sequence number inside each millisecond). Across replicas, IDs made in the same millisecond may interleave; paging stays correct, "oldest first" is just approximate to the millisecond
- copies (clones, forks) get derived IDs instead (`dam.CopyAssetID`, `CopyFolderID`, `CopyObjectID`), so a retried copy lands on the same rows and documents can compute a clone's refs. They're shaped like UUIDv7s: the 48-bit timestamp is the project's ID's (the clone, or the project a fork is made in), the rest a SHA-256 of the inputs. A project's copies sort together near the project's creation, not at copy time

## DAM

Assets are named, tagged files in a user's, org's or project's library.
Types and the `Store` interface are in `assets.go`; the rules every Store
enforces are in `assets_rules.go`; limits are constants at the top of `assets.go`.

- **asset**: owner (same `Owner` type and rules as the ledger, via `Owner.Validate`), optional `ProjectID` (association, as in the ledger), folder, name, description, tags, content type, category, current version, timestamps
- **versions**: each upload of a file is a version pointing at a ledger object. Append-only: a new upload becomes version N+1 and current; "revert" makes an older version current again without copying anything; reverting to the current version changes nothing (not even `UpdatedAt`), so retries are harmless. Versions are removed only together with their asset
- **tags**: free-form, normalised (trimmed, lowercased, deduplicated, sorted), so "Logo", " logo" and "LOGO" are one tag. A set per asset; `Tags` in an update replaces the whole set
- **categories**: `image`, `vector`, `video`, `audio`, `font`, `pdf`, `json`, `text`, `other`, derived from the MIME type with `CategoryOf`, so an asset's kind is known without reading bytes or asking the ledger. Fixed for the asset's life: a new version must have the same category (png -> jpeg yes, image -> video no)
- **document snapshots are not assets**: nobody names, tags or re-uploads them, and their history is the document's own log. They live only in the ledger (`Kind=snapshot`), written through storage by documents. A `document` category comes only if library templates/components are designed later
- **listing**: exactly one scope (project or owner) plus optional tag and category filters; oldest first, exact thanks to UUIDv7. Folder listing arrives with folders; name search later
- **deletion**: soft. A deleted asset stays readable (get, versions, list with `IncludeDeleted`) but can't be changed: update, add version and revert return `ErrAssetNotFound`
- **asset names aren't unique**: an asset is identified by its ID, so its name is just a label and several assets in one folder may share it. Wherever a unique name is needed it's resolved on the way out: browsers number duplicate downloads themselves, and a future zip export or sync should number them as it writes ("Logo.png", "Logo (2).png")
- **folders**: a tree per owner (adjacency list in Postgres; S3 keys never change). Sibling names unique ignoring case, portable across Windows/macOS/Linux, max depth 32, no cycles on move
- **restore**: a restored asset whose folder is gone lands at the top level; a restored folder whose parent is gone lands at the top level, and one whose name is now taken by a live sibling is numbered "Name (2)", "Name (3)", ... (the same rule ownership transfers use)
- **trash**: deleting folders or assets moves them to the trash (soft delete) and keeps their place in the tree, so restoring puts them back. `DeleteFolder` only deletes empty folders; if not empty it returns a `FolderNotEmptyError` with counts so the UI can offer rm -rf, which is `DeleteFolderTree`: the folder, its sub-folders and their live assets, all at once under one `DeletionID` so the whole thing can be undone. Bytes stay until the cleanup job purges them (see below)
- **partial updates**: `AssetUpdate` fields are pointers; `nil` leaves a field alone, a pointer to `""` or an empty slice clears it
- **versions never change**: a version's object, content type, size and creation time are fixed for good (only `CreatedBy` is cleared, for GDPR), so a pinned ref always means the same bytes. The store suite locks this (`VersionsAreImmutable`)
- **chosen IDs**: `CreateAsset` and `CreateFolder` take a caller-chosen ID and return the existing row on a repeat; copies rely on it

## Asset refs, uses and copies

Built 2026-10-12, for cloning projects and for documents that use library files.

- **refs**: a document points at `AssetRef{AssetID, Version}`, always pinned (`Version >= 1`), written `"id@3"` (`MarshalText`, so also as a JSON map key; one text form per ref). New uploads and reverts never move a ref; "update available" is `ref.Version < asset.CurrentVersion`
- **uses**: `storage.asset_uses` (migration 00004) records which versions each project uses: `(project, asset, version)`, kept by documents through `RecordAssetUse` / `ForgetAssetUse` (idempotent; for the user editing the document, service to service). Purging an asset drops its uses (`ON DELETE CASCADE` from its versions); deleting a project forgets its own
- **reading through a project**: `ResolveRef(actor, project, ref)` returns the asset, the version and a download link. Allowed if the actor reads the project and either it uses that exact version or the actor reads the asset directly. A used asset keeps resolving from the trash (`DeletedAt` set, for the UI) until the purge; direct access serves live assets only, like `DownloadURL`. `ListTrash` reports how many projects use each trashed asset
- **rule A, what a project may use in place**: its own files, its owner's library (same user or org), and modulith's shared files (nobody can delete those). Anything else is refused with `ErrForkRequired` (together with `ErrForbidden`): the client forks it. So no project depends on files another owner controls and may purge. Recording a use also needs write on the project and read on the asset. The project's owner comes from iam (`Authorizer.ProjectOwner`)
- **forks**: `ForkAsset(actor, project, ref, target, via)` copies one version into a new asset owned by the project (default) or its owner, and moves the project's use to it. The actor needs write on the project, and read on the version through the project's use, through project `via`'s use (copying out of a shared project: no more than they could download), or directly. A used asset in the trash can be forked, rescuing it before the purge
- **clones**: `CopyProjectFiles(actor, from, to)`, for iam's `CloneProject` (which checks permission; storage only records the cloner as creator). Copies the folder tree, every live asset the source owns at its current version, and every version it uses (whoever owns it, trashed ones too), and records the clone's uses of the copies. Each copy is version 1 of a new asset, independent of the source both ways: deleting and purging the source leaves the clone intact
- **`Asset.ProjectID` stays** (decided with uses): it means "created for this project". The project grants read on it, never write, and attaching a new file to a project needs write on the project; uses are what a document actually references
- **copies never share files**: each gets its own ledger object and S3 key (`blob.Store.Copy`, server-side, up to 5 GiB), because purging an object deletes its file. Steps, each repeatable: ledger object first (so no file is ever untracked), copy the bytes, copy the media info, create the asset last (the "done" marker). An interrupted clone resumes on the next call without duplicates

## Completing uploads safely

`CompleteUpload` may be called again (retry, timeout, double click), even overlapping, and always ends with one asset (or version) and exactly one ledger object:

- `CreateUpload` chooses the ledger object's ID up front (`Upload.ObjectID`); `CompleteUpload` creates the ledger object under that ID. The ledger's `Create` is idempotent for a caller-chosen ID (same file again: the existing object), so retries, overlapping calls and a resumed attempt that crashed after the ledger step all land on the same object. No duplicates exist, so no S3 key is ever shared by two ledger objects, which is what makes purging a file safe
- a completed session returns its asset again, even after it expired; the Store lets one of several overlapping calls complete the session and hands the others the same asset
- rejected files (wrong bytes, wrong size) stay in S3 until the session expires and the cleanup job deletes them
- an earlier design recorded the ID after creating the object (first one wins) and retired losers' duplicates; it left a crash gap and duplicates sharing an S3 key, so it was replaced

Tests force real overlap with wrappers that embed the memory fakes and hold calls at a barrier (`gatedBlobs`, `gatedStore`), since in-memory calls are too fast to overlap by chance.

## Cleanup and retention

`Manager.Cleanup(ctx, now)` removes what should disappear on its own; `RunCleanup` runs it at start-up and then every `MODULITH_CLEANUP_INTERVAL` (default 10 min) in every binary hosting the Manager. In order:

1. **expired uploads** (expired more than 5 min ago, a margin for replicas' clock skew): delete the file the browser put in S3, purge the ledger object if a completion got that far, delete the session
2. **completed upload sessions** older than the retention period
3. **trashed assets** older than the retention period: for each version, delete the file and purge its ledger object, then purge the asset and its versions from the Store
4. **trashed folders** older than the retention period (anything trashed still inside them moves to the top level)
. **abandoned snapshot uploads** (pending past their expiry, less the skew margin): delete the file, purge the ledger object if a completion got that far, delete the record
7. **old snapshots** beyond each project's newest `MODULITH_SNAPSHOT_KEEP` (default 100): deleted at once, not trashed: they're past the revert window by definition

- **retention**: `MODULITH_TRASH_RETENTION`, default 7 days (168h), at least 1h
- **restore ends where purging starts**: `RestoreAsset`/`RestoreFolder` refuse items whose retention has run out (less the 5 min skew margin) with not-found, and only those items are purged, so a restore never races the deletion of its files
- **always files first, records last**: a run that dies part-way leaves the record, so the next run finds it again and skips what's already gone. Every step is idempotent, so several replicas may run the job at once (they only repeat each other's work)
- a run stops at the first failure and reports what it did; the next run retries from there (see OPTIMIZATIONS.md for the "stuck item" caveat)
- known limitation: restoring an asset trashed by `DeleteOwned` (its owner was deleted) doesn't bring its ledger objects back; after the retention period the ledger step deletes their files. Owners don't come back, so nothing can restore those today

## Document snapshots

Written by the documents service only (no actor: its saves run on a timer; no gateway routes). This is a security rule, not just today's wiring: the engine trusts every stored snapshot's stamps (no `MaxSkew` bound, [engine/PLAN.md](../../internal/engine/PLAN.md) row 2), and documents and browsers both merge them, so `BeginSnapshot`/`CompleteSnapshot` must never be reachable by a browser. Code: `internal/snapshots` (the `storage.snapshots` table, migration 00005; `Memory`, `Postgres`, shared suite) and `internal/manager/manager_snapshots.go`.

- **two-phase, like uploads:** `BeginSnapshot(projectID, seq, editedBy, editedAt, contentType, size)` checks the project exists (`ProjectOwner`), the type (`application/vnd.modulith.app+flatbuffers` or `application/json`, with parameters) and the size (`MODULITH_SNAPSHOT_MAX_BYTES`, default 32 MiB), records a pending row and presigns a PUT to `projects/{project}/snapshot/{id}`. `CompleteSnapshot` checks the object in S3 (size and type), records it in the ledger (`Kind=snapshot`, owned by the project, `CreatedBy` = the last editor), then gives it the next version. It returns only once S3 holds the object, which is what lets documents compact its deltas
- **never back in time:** a snapshot covering an earlier delta stream sequence than the project's latest is refused (`ErrSnapshotStale`, gRPC `FAILED_PRECONDITION`/`STALE`), early at Begin and atomically at Complete (a per-project advisory lock, plus `UNIQUE (project_id, version)`). A refused one's object and ledger row are removed. Equal sequences are allowed
- **last edit, `touch`-style:** each snapshot records who made the last change it holds and when (`edited_by`, `edited_at`). Informational, not authorised. Cleared when the user is deleted (`RemoveUser`)
- `LatestSnapshot`, `ListSnapshots` (newest first, 50 a page, for administrative reverts), `SnapshotURL` (presigned GET; only the project's own, completed snapshots)
- deleting a project trashes its snapshot objects with its other files (the project owns them) and removes the records (`DeleteOwned`)

## API

- in-process: the `Service` interface (`storage.go`, defined in `internal/manager/service.go`), implemented by `*Manager`. Callers (the gateway, iam, documents) depend on `Service`, never on `*Manager`
- over the network: `StorageService` in `contracts/proto/modulith/storage/v1/storage.proto`. `GRPCServer` exposes any `Service`; `GRPCClient` is a `Service` calling one remotely, with the same types and errors. The ledger has no API of its own
- every call carries `actor_id`, set by the gateway from the signed-in session; the service trusts it, so it must only be reachable from inside the platform
- errors: there are more sentinels than gRPC has codes, so each status carries a `google.rpc.ErrorInfo` per matching sentinel (domain `storage.modulith`, reason e.g. `ASSET_NOT_FOUND`; `FOLDER_NOT_EMPTY` adds `folders`/`assets` counts); `FORK_REQUIRED` always comes with `FORBIDDEN`, so clients that only know "forbidden" still refuse. The client rebuilds them, so `errors.Is(err, ErrNameTaken)` and `errors.As(err, &notEmpty)` work across the wire. The table is `wireErrors` in `internal/manager/grpc.go`. Unexpected errors are logged on the server and sent as a bare INTERNAL
- binaries: the monolith (`cmd/modulith`) and `cmd/storage` both build it with `storage.Open` (`wiring.go`: settings, S3, migrations, and the `Authorizer` to ask) and serve `StorageService` with `storage.RegisterGRPC`
- settings: `MODULITH_S3_BUCKET`, `MODULITH_S3_ACCESS_KEY`, `MODULITH_S3_SECRET_KEY` (required); `MODULITH_S3_ENDPOINT` (empty for AWS), `MODULITH_S3_REGION`, `MODULITH_S3_PATH_STYLE`, `MODULITH_S3_CREATE_BUCKET` (local only), `MODULITH_UPLOAD_URL_TTL`, `MODULITH_DOWNLOAD_URL_TTL`, `MODULITH_TRASH_RETENTION`, `MODULITH_CLEANUP_INTERVAL`
- authorization: iam's `StorageAuthorizer`, in-process in the monolith; `cmd/storage` asks `cmd/iam` over gRPC (`MODULITH_IAM_ADDR`, `iam.NewAuthzClient`). While iam is unreachable, calls fail rather than being allowed

## Authorization

- the gateway authenticates (who is calling); services authorize (what they may do)
- Manager methods take the acting user's ID and ask an `Authorizer` interface declared here: may this user read/write files for this owner or project?
- iam implements it: org existence, membership and project sharing (allowed users) all live there. storage never reads iam's tables. `AllowAll` remains for tests only
- a project grants write only to files it owns; files merely associated with it (`ProjectID`) are read-only through it. `RuleAuthorizer` (tests) follows the same rule as iam, so tests can't pass flows production refuses
- creating a file associated with a project needs write on that project too, not just on the owner: otherwise anyone could put their own files into any project's listings (`canCreateIn`; a new version keeps its asset's association)
- `ProjectOwner(project)` tells storage who owns a project, for rule A (see Asset refs)
- the routes around an owner were audited on 2026-10-12; `manager_access_test.go` pins each closed route
- the `Store` does no authorization: it only checks data shape and trusts the Manager

## Layout

| Path                      | What                                                                                                                                                                                                                                                            |
| ------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `storage.go`, `wiring.go` | the public surface: type aliases (`Owner`, `Service`, `Config`, ...), constructors, `Migrate`, `RegisterGRPC`, `NewGRPCClient`; `Settings`/`SettingsFromEnv`, `Open`, `StartCleanup` for the binaries                                                           |
| `internal/db/`            | the `storage` schema: `migrations/` (goose, embedded, `Migrate`), `ledger.sql`, `manager.sql`, `uses.sql` and `snapshots.sql` (queries), `sqlc.yaml` and the generated code. Regenerate with `go generate ./services/storage/...`                                                            |
| `internal/ledger/`        | the ledger: `Service` with `Memory` and `Postgres`, shared suite `runServiceTests`                                                                                                                                                                              |
| `internal/snapshots/`     | document snapshot records: `Store` with `Memory` and `Postgres`, shared suite `runStoreTests`                                                                                                                                                                   |
| `internal/dam/`           | the DAM data: the `Store` interface, types, rules, `MemoryStore` and `Postgres` (files below)                                                                                                                                                                   |
| `internal/manager/`       | the logic: `Manager`, `Authorizer`, the `Service` interface (files below)                                                                                                                                                                                       |
| `internal/grpcapi/`       | `StorageService` adapters: `Server` wraps any `Service`, `Client` is a `Service` over the wire (files below)                                                                                                                                                    |
| `internal/blob/`          | file storage the Manager uses (internal, so only storage can reach S3): `blob.Store` (presign PUT/GET with download filename, stat, read first bytes, delete, server-side copy), `blob.S3` (aws-sdk-go-v2, tested against RustFS), `blob.Memory` fake, shared suite in `blobtest` |

### `internal/dam`

| File                                                  | What                                                                                                                                                                                                                                                                                                                                                                                                            |
| ----------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `assets.go`                                           | errors, limits, DAM types, `CategoryOf`, `Store` interface                                                                                                                                                                                                                                                                                                                                                      |
| `assets_rules.go`                                     | validation, normalisation and paging helpers shared by every Store                                                                                                                                                                                                                                                                                                                                              |
| `assets_memory.go`                                    | `MemoryStore`                                                                                                                                                                                                                                                                                                                                                                                                   |
| `folders.go`, `folders_rules.go`, `folders_memory.go` | folder tree: `Folder`, `FolderStore` (embedded in `Store`), portable-name rules, trash, rm -rf (`DeleteFolderTree`)                                                                                                                                                                                                                                                                                             |
| `uploads.go`, `uploads_rules.go`, `uploads_memory.go` | upload sessions: `Upload` (with the ledger object ID chosen at creation), `UploadStore` (embedded in `Store`); `CompleteUpload` creates the asset or version and closes the session atomically, and is idempotent                                                                                                                                                                                               |
| `store_folders_test.go`, `store_uploads_test.go`      | folder and upload cases of the shared store suite                                                                                                                                                                                                                                                                                                                                                               |
| `owners.go`, `owners_memory.go`                       | `OwnerStore` (embedded in `Store`): `TransferOwner` (trees move whole; clashing top-level names numbered), `MarkDeletedByOwner`, `ClearCreatedBy`, `ExpireUploadsBy`                                                                                                                                                                                                                                            |
| `store_owners_test.go`                                | store-suite cases for the owner lifecycle, including ownership moving mid-upload                                                                                                                                                                                                                                                                                                                                |
| `store_trash_test.go`                                 | store-suite cases for `GetVersion`, the trash filter, restores and breadcrumbs                                                                                                                                                                                                                                                                                                                                  |
| `postgres*.go`                                        | the Postgres `Store` (queries in `internal/db/manager.sql`): runs the same shared suite as `MemoryStore` (`postgres_test.go`, needs `MODULITH_TEST_DATABASE_URL`). Timestamps come from Go; folder names unique via a partial unique index on `name_key` (`NULLS NOT DISTINCT`); per-owner advisory lock for tree changes; row locks for assets and upload sessions; `WITH RECURSIVE` for breadcrumbs and subtrees |
| `store_test.go`                                       | shared `runStoreTests` suite (MemoryStore and Postgres)                                                                                                                                                                                                                                                                                                                                                         |
| `purge.go`, `purge_memory.go`, `postgres_purge.go`    | `PurgeStore` (embedded in `Store`): trash listed by age, assets and folders purged for good, old completed sessions deleted                                                                                                                                                                                                                                                                                     |
| `uses.go`, `uses_rules.go`, `uses_memory.go`, `postgres_uses.go` | `UseStore` (embedded in `Store`): record, forget, swap (a fork), check, list and count asset uses; the Postgres side locks the asset `FOR SHARE` so it can't be trashed mid-record |
| `store_uses_test.go` | store-suite cases for uses and chosen IDs |
| `*_rules_test.go`                                     | internal unit tests of the unexported helpers                                                                                                                                                                                                                                                                                                                                                                   |

### `internal/manager`

| File                  | What                                                                                                                                                                                                                                                                                                                             |
| --------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `authz.go`            | `Authorizer` interface (implemented by iam; `ProjectOwner` included), `ErrForbidden`, `ErrForkRequired`, `AllowAll` (tests), `RuleAuthorizer` for tests and local runs                                                                                                                                                                                                |
| `manager.go`          | `Manager` (business logic over ledger, Store, Blobs, Authorizer), `Config` with defaults and S3 limits, `NewManager`                                                                                                                                                                                                             |
| `manager_upload.go`   | `BeginUpload` (permission, key, pending session, presigned PUT ticket), `objectKey` (the only place keys are built)                                                                                                                                                                                                              |
| `manager_complete.go` | `CompleteUpload`: creator + permission, idempotent result, expiry, `Stat` + sniff, ledger object under the session's chosen ID (idempotent), then `Store.CompleteUpload`                                                                                                                                                         |
| `manager_download.go` | `DownloadURL` (asset version, 0 = current; read permission checked before the version lookup) and `ExportURL` (ledger objects of `Kind=export` only); `downloadFilename` (portable name + extension from a fixed MIME table)                                                                                                     |
| `manager_dam.go`      | file-browser operations: assets (get, list, update/move, delete, restore, versions, revert), folders (create, rename, move, delete, rm -rf, restore, breadcrumbs), `ListFolderContents` (breadcrumbs + sub-folders on the first page + paged assets), `ListTrash`. Project-owned items authorize through the project (`scopeOf`) |
| `manager_owners.go`   | service-to-service lifecycle (iam calls these; never exposed to browsers): `DeleteOwned` (project/org/org-less user), `RemoveUser` (expire their uploads, transfer to org or trash, clear creator everywhere)                                                                                                                    |
| `sniff.go`            | `checkSniffed`: do a file's first 512 bytes match its declared type? Rejects only clear mismatches (`ErrContentMismatch`)                                                                                                                                                                                                        |
| `manager_cleanup.go`  | the cleanup job: `Cleanup` (expired uploads, completed sessions, trash, deleted ledger objects; files first, records last), `RunCleanup` (the timer), `restorable` (the restore cut-off)                                                                                                                                         |
| `manager_uses.go` | `RecordAssetUse` / `ForgetAssetUse` (rule A, `ErrForkRequired`) |
| `manager_resolve.go` | `ResolveRef`: reading a ref through a project |
| `manager_copy.go` | `copyVersion` (the one copy path), `CopyProjectFiles` (clones), `ForkAsset` |
| `manager_access_test.go` | the closed routes around an owner (attaching to a project, recording uses) |
| `service.go`          | `Service`: what the gateway and iam call; `*Manager` and `GRPCClient` implement it                                                                                                                                                                                                                                               |
| `export_test.go`      | test-only hooks (`SetClock`, constants): in package `manager` but compiled only into tests                                                                                                                                                                                                                                       |
| `helpers_test.go`     | owners and store helpers the Manager tests share                                                                                                                                                                                                                                                                                 |

### `internal/grpcapi`

| File                                          | What                                                                                                                |
| --------------------------------------------- | ------------------------------------------------------------------------------------------------------------------- |
| `grpc.go`, `grpc_server.go`, `grpc_client.go` | gRPC adapters: conversions, the `wireErrors` table (status code + ErrorInfo reason per sentinel), server and client |
| `grpc_test.go`, `grpc_internal_test.go`       | every call through a real client and server (bufconn) compared with the Manager directly; every error round-trips   |
| `harness_test.go`                             | a Manager on memory fakes for the round-trip tests                                                                  |

Everything was mutation-checked: realistic bugs reintroduced one at a time are each caught by a named test (blobs 3/3, assets 9/9, folders 13/13, uploads 12/12, Manager steps 4-6 all caught, Postgres store 16/16, the upload rework 4/4, cleanup and purge 26/27, gRPC adapters 18/18; phase 2: refs 5/5, version immutability 4/4, blob copy 4/7, uses 12/12, access fixes 11/11, rule A 8/8, `ResolveRef` 8/8, copies and forks 17/17, via 4/4, refs over gRPC 9/9). The blob-copy survivors can't be caught locally: the memory fake never mutates stored bytes (so its clone is unobservable), and RustFS accepts both unescaped and over-escaped `CopySource`, which AWS rejects. The one other survivor is unobservable: MemoryStore keeping a purged asset's versions in its map. Run mutation checks with `go test -timeout 60s`: a bug that corrupts the folder tree can make walks loop forever.

## Media metadata

An image's, video's or audio file's properties (size, duration, codecs) are extracted once when it's stored, so they can be read without fetching the file. Built 2026-10-12.

- **per ledger object**: `storage.media_info` (migration 00002), keyed by object ID with `ON DELETE CASCADE`, so purging an object drops it. A version points at one object, so this is per version (a replacement file can have a different resolution), and any ledger object can have it. Calls on `ledger.Service`: `MarkMediaPending`, `SetMediaInfo`, `GetMediaInfo`, `ListPendingMedia` (`ledger/media.go`)
- **columns**: `status` (`pending`, `ready`, `failed`), typed `width`/`height` (display size, after rotation) and `duration_us` for filtering and sorting, 0 = unknown; the rest as `details` jsonb (camelCase), so new fields need no migration; `error` and `attempts` for failures
- **raster images** (`manager_image.go`): measured inline in `CompleteUpload` from the same `ReadPrefix` as the content check, 256 KiB for images (JPEG metadata can push the size marker back). `image.DecodeConfig` reads only the header: JPEG, PNG, GIF, WebP (`golang.org/x/image`). JPEG EXIF orientation 5-8 swaps to the display size; the EXIF parser treats the bytes as hostile (bounds-checked, fuzzed). Formats without a decoder (HEIC, AVIF, BMP) or broken headers are recorded `failed`; the upload succeeds either way
- **vector images**: `image/svg+xml` is its own category, `vector` (migration 00003 moved existing SVGs), with no media info: SVGs have no pixel size
- **video and audio** (`manager_media.go`): `CompleteUpload` marks them `pending`; the probe job (`RunMediaProbe`, every `MODULITH_PROBE_INTERVAL`) presigns a 2-minute GET and calls `mediaprocessor.Prober` (ffprobe over HTTP range requests, no S3 credentials). Three failed attempts mark it `failed`; a probe cut off by shutdown doesn't count. Neither does a mediaprocessor that can't be reached (`mediaprocessor.ErrUnavailable`): files wait for it. The monolith probes in-process (only with ffprobe on PATH); `cmd/storage` calls `cmd/mediaprocessor` over gRPC at `MODULITH_MEDIAPROCESSOR_ADDR`.
- **retries**: both are recorded before the upload session completes, and both calls are safe to repeat, so a retried `CompleteUpload` records them again
- not yet: media info on `Version` and in `storage.proto`; SVG size from `viewBox`; HEIC/AVIF via ffprobe; whether exports get media info
