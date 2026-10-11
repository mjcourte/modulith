# Storage Manager

- General S3 putter/getter; records every object in the ledger (`storageledger`, same deployable, called in-process)
- uploads/user assets/etc
- not concerned with document files

## Decisions

- uploads and downloads use presigned S3 URLs: the browser talks to S3 directly, our servers never carry file bytes
- upload is two-phase: begin (pending session + presigned PUT that signs size and content type), then complete (verify the blob, record it in the ledger)
- HTTP routes live in the gateway; this service exposes a Go `Service` and the same over gRPC, like the ledger (see API)
- DAM metadata (names, descriptions, tags, folders, versions) lives in this service's own Postgres schema; each asset version points at a ledger object ID
- user removal: transfer the user's objects to their org (a user has at most one), or delete them if they have none
- trash is kept 7 days, then purged for good by a cleanup job that runs in every binary hosting the Manager (see Cleanup and retention)

## S3 key layout

Keys only contain facts that never change and aren't personal data. S3 can't
rename (a "move" is copy + delete), so anything that can change, like the owner
or the DAM folder, stays out of the key; the ledger is the index for those.

```sh
projects/{projectID}/{kind}/{id}    objects associated with a project
library/{kind}/{id}                 objects with no project (user/org asset libraries)
```

- `kind`: the ledger `Kind` (`asset`, `export`, `snapshot`)
- `id`: a random ID that makes the key unique
- never in a key: user IDs, org IDs, owner kind, folder names, file names
- a project ID must be a single plain path segment (no `/`, `\`, `.` or `..`), or the upload is refused: otherwise it could produce a key outside the layout
- why this shape: per-project browsing in the S3 console, lifecycle rules by prefix (e.g. expire `projects/*/export/`), bulk delete by project prefix if ever needed, and one prefix per project spreads S3's per-prefix request limits
- the Manager is the only code that builds keys (one function); callers never pass a key in. Other services that write to S3 (document manager snapshots, exports) must use the same layout

## URL lifetimes

- `blob.Store` (`platform/blob`) takes an explicit TTL (mechanism); how long URLs last is Manager policy
- defaults: upload URLs 15 min, download URLs 5 min, overridable with `GYOZA_UPLOAD_URL_TTL` / `GYOZA_DOWNLOAD_URL_TTL`
- S3 checks expiry when a request starts, so a long upload that starts in time still completes

## IDs

- presign tokens (memory fake): `crypto/rand.Text()`, since they only need to be unguessable
- table IDs (assets, and later folders and upload sessions): UUIDv7 via `github.com/google/uuid`. They start with a timestamp, so ID order is creation order: paging orders by ID alone, and Postgres inserts land at the end of the index (cheaper than the ledger's random IDs, see storageledger/OPTIMIZATIONS.md)
- ordering is guaranteed within one process (the library adds a sequence number inside each millisecond). Across replicas, IDs made in the same millisecond may interleave; paging stays correct, "oldest first" is just approximate to the millisecond

## DAM

Assets are named, tagged files in a user's, org's or project's library.
Types and the `Store` interface are in `assets.go`; the rules every Store
enforces are in `assets_rules.go`; limits are constants at the top of `assets.go`.

- **asset**: owner (same `Owner` type and rules as the ledger, via `Owner.Validate`), optional `ProjectID` (association, as in the ledger), folder, name, description, tags, content type, category, current version, timestamps
- **versions**: each upload of a file is a version pointing at a ledger object. Append-only: a new upload becomes version N+1 and current; "revert" makes an older version current again without copying anything; reverting to the current version changes nothing (not even `UpdatedAt`), so retries are harmless. Versions are removed only together with their asset
- **tags**: free-form, normalised (trimmed, lowercased, deduplicated, sorted), so "Logo", " logo" and "LOGO" are one tag. A set per asset; `Tags` in an update replaces the whole set
- **categories**: `image`, `video`, `audio`, `font`, `pdf`, `json`, `text`, `other`, derived from the MIME type with `CategoryOf`, so an asset's kind is known without reading bytes or asking the ledger. Fixed for the asset's life: a new version must have the same category (png -> jpeg yes, image -> video no)
- **document snapshots are not assets**: nobody names, tags or re-uploads them, and their history is the document's own log. They live only in the ledger (`Kind=snapshot`). A `document` category comes only if library templates/components are designed later
- **listing**: exactly one scope (project or owner) plus optional tag and category filters; oldest first, exact thanks to UUIDv7. Folder listing arrives with folders; name search later
- **deletion**: soft. A deleted asset stays readable (get, versions, list with `IncludeDeleted`) but can't be changed: update, add version and revert return `ErrAssetNotFound`
- **asset names aren't unique**: an asset is identified by its ID, so its name is just a label and several assets in one folder may share it. Wherever a unique name is needed it's resolved on the way out: browsers number duplicate downloads themselves, and a future zip export or sync should number them as it writes ("Logo.png", "Logo (2).png")
- **folders**: a tree per owner (adjacency list in Postgres; S3 keys never change). Sibling names unique ignoring case, portable across Windows/macOS/Linux, max depth 32, no cycles on move
- **restore**: a restored asset whose folder is gone lands at the top level; a restored folder whose parent is gone lands at the top level, and one whose name is now taken by a live sibling is numbered "Name (2)", "Name (3)", ... (the same rule ownership transfers use)
- **trash**: deleting folders or assets moves them to the trash (soft delete) and keeps their place in the tree, so restoring puts them back. `DeleteFolder` only deletes empty folders; if not empty it returns a `FolderNotEmptyError` with counts so the UI can offer rm -rf, which is `DeleteFolderTree`: the folder, its sub-folders and their live assets, all at once under one `DeletionID` so the whole thing can be undone. Bytes stay until the cleanup job purges them (see below)
- **partial updates**: `AssetUpdate` fields are pointers; `nil` leaves a field alone, a pointer to `""` or an empty slice clears it

## Completing uploads safely

`CompleteUpload` may be called again (retry, timeout, double click), even overlapping, and always ends with one asset (or version) and exactly one ledger object:

- `CreateUpload` chooses the ledger object's ID up front (`Upload.ObjectID`); `CompleteUpload` creates the ledger object under that ID. The ledger's `Create` is idempotent for a caller-chosen ID (same file again: the existing object), so retries, overlapping calls and a resumed attempt that crashed after the ledger step all land on the same object. No duplicates exist, so no S3 key is ever shared by two ledger objects, which is what makes purging a file safe
- a completed session returns its asset again, even after it expired; the Store lets one of several overlapping calls complete the session and hands the others the same asset
- rejected files (wrong bytes, wrong size) stay in S3 until the session expires and the cleanup job deletes them
- an earlier design recorded the ID after creating the object (first one wins) and retired losers' duplicates; it left a crash gap and duplicates sharing an S3 key, so it was replaced

Tests force real overlap with wrappers that embed the memory fakes and hold calls at a barrier (`gatedBlobs`, `gatedStore`), since in-memory calls are too fast to overlap by chance.

## Cleanup and retention

`Manager.Cleanup(ctx, now)` removes what should disappear on its own; `RunCleanup` runs it at start-up and then every `GYOZA_CLEANUP_INTERVAL` (default 10 min) in every binary hosting the Manager. In order:

1. **expired uploads** (expired more than 5 min ago, a margin for replicas' clock skew): delete the file the browser put in S3, purge the ledger object if a completion got that far, delete the session
2. **completed upload sessions** older than the retention period
3. **trashed assets** older than the retention period: for each version, delete the file and purge its ledger object, then purge the asset and its versions from the Store
4. **trashed folders** older than the retention period (anything trashed still inside them moves to the top level)
5. **other deleted ledger objects** older than the retention period (e.g. a deleted project's exports): delete the file, purge the object

- **retention**: `GYOZA_TRASH_RETENTION`, default 7 days (168h), at least 1h
- **restore ends where purging starts**: `RestoreAsset`/`RestoreFolder` refuse items whose retention has run out (less the 5 min skew margin) with not-found, and only those items are purged, so a restore never races the deletion of its files
- **always files first, records last**: a run that dies part-way leaves the record, so the next run finds it again and skips what's already gone. Every step is idempotent, so several replicas may run the job at once (they only repeat each other's work)
- a run stops at the first failure and reports what it did; the next run retries from there (see OPTIMIZATIONS.md for the "stuck item" caveat)
- known limitation: restoring an asset trashed by `DeleteOwned` (its owner was deleted) doesn't bring its ledger objects back; after the retention period the ledger step deletes their files. Owners don't come back, so nothing can restore those today

## API

- in-process: the `Service` interface (`service.go`), implemented by `*Manager`. Callers (the gateway, iam) depend on `Service`, never on `*Manager`
- over the network: `StorageManagerService` in `contracts/proto/gyoza/storagemanager/v1/manager.proto`. `GRPCServer` exposes any `Service`; `GRPCClient` is a `Service` calling one remotely, with the same types and errors
- every call carries `actor_id`, set by the gateway from the signed-in session; the service trusts it, so it must only be reachable from inside the platform
- errors: storagemanager has more sentinels than gRPC has codes, so each status carries a `google.rpc.ErrorInfo` per matching sentinel (domain `storagemanager.gyoza`, reason e.g. `ASSET_NOT_FOUND`; `FOLDER_NOT_EMPTY` adds `folders`/`assets` counts). The client rebuilds them, so `errors.Is(err, ErrNameTaken)` and `errors.As(err, &notEmpty)` work across the wire. The table is `wireErrors` in `grpc.go`. Unexpected errors are logged on the server and sent as a bare INTERNAL
- binaries: the monolith (`cmd/gyoza`) runs it in-process with the ledger in-process; `cmd/storage` runs it with the ledger in-process and serves both gRPC APIs (one storage service, decided 2026-10-11). Shared wiring (settings, S3, migrations) is in `cmd/internal/wiring`
- settings: `GYOZA_S3_BUCKET`, `GYOZA_S3_ACCESS_KEY`, `GYOZA_S3_SECRET_KEY` (required); `GYOZA_S3_ENDPOINT` (empty for AWS), `GYOZA_S3_REGION`, `GYOZA_S3_PATH_STYLE`, `GYOZA_S3_CREATE_BUCKET` (local only), `GYOZA_UPLOAD_URL_TTL`, `GYOZA_DOWNLOAD_URL_TTL`, `GYOZA_TRASH_RETENTION`, `GYOZA_CLEANUP_INTERVAL`
- authorization is `AllowAll` in the binaries until iam exists (logged as a warning at start-up)

## Authorization

- the gateway authenticates (who is calling); services authorize (what they may do)
- Manager methods (step 4) take the acting user's ID and ask an `Authorizer` interface declared here: may this user read/write files for this owner or project?
- iam implements it: org existence, membership and project sharing (allowed users) all live there. storagemanager never reads iam's tables. Until iam exists, an allow-all stand-in is used
- the `Store` does no authorization: it only checks data shape and trusts the Manager

## Code map

| File                                                                                    | What                                                                                                                                                                                                                                                                                                                                                                                               |
| --------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| (`platform/blob`)                                                                       | file storage the Manager uses: `blob.Store` (presign PUT/GET with download filename, stat, read first bytes, delete), `blob.S3` (aws-sdk-go-v2, tested against RustFS), `blob.Memory` fake, shared suite in `blobtest`                                                                                                                                                                             |
| `assets.go`                                                                             | errors, limits, DAM types, `CategoryOf`, `Store` interface                                                                                                                                                                                                                                                                                                                                         |
| `assets_rules.go`                                                                       | validation, normalisation and paging helpers shared by every Store                                                                                                                                                                                                                                                                                                                                 |
| `assets_memory.go`                                                                      | `MemoryStore`                                                                                                                                                                                                                                                                                                                                                                                      |
| `folders.go`, `folders_rules.go`, `folders_memory.go`                                   | folder tree: `Folder`, `FolderStore` (embedded in `Store`), portable-name rules, trash, rm -rf (`DeleteFolderTree`)                                                                                                                                                                                                                                                                                |
| `uploads.go`, `uploads_rules.go`, `uploads_memory.go`                                   | upload sessions: `Upload` (with the ledger object ID chosen at creation), `UploadStore` (embedded in `Store`); `CompleteUpload` creates the asset or version and closes the session atomically, and is idempotent                                                                                                                                                                                  |
| `store_folders_test.go`, `store_uploads_test.go`                                        | folder and upload cases of the shared store suite                                                                                                                                                                                                                                                                                                                                                  |
| `authz.go`                                                                              | `Authorizer` interface (implemented by iam later), `ErrForbidden`, `AllowAll` stand-in, `RuleAuthorizer` for tests and local runs                                                                                                                                                                                                                                                                  |
| `manager.go`                                                                            | `Manager` (business logic over ledger, Store, Blobs, Authorizer), `Config` with defaults and S3 limits, `NewManager`                                                                                                                                                                                                                                                                               |
| `manager_upload.go`                                                                     | `BeginUpload` (permission, key, pending session, presigned PUT ticket), `objectKey` (the only place keys are built)                                                                                                                                                                                                                                                                                |
| `manager_complete.go`                                                                   | `CompleteUpload`: creator + permission, idempotent result, expiry, `Stat` + sniff, ledger object under the session's chosen ID (idempotent), then `Store.CompleteUpload`                                                                                                                                                                                                                           |
| `manager_download.go`                                                                   | `DownloadURL` (asset version, 0 = current; read permission checked before the version lookup) and `ExportURL` (ledger objects of `Kind=export` only); `downloadFilename` (portable name + extension from a fixed MIME table)                                                                                                                                                                       |
| `manager_dam.go`                                                                        | file-browser operations: assets (get, list, update/move, delete, restore, versions, revert), folders (create, rename, move, delete, rm -rf, restore, breadcrumbs), `ListFolderContents` (breadcrumbs + sub-folders on the first page + paged assets), `ListTrash`. Project-owned items authorize through the project (`scopeOf`)                                                                   |
| `owners.go`, `owners_memory.go`                                                         | `OwnerStore` (embedded in `Store`): `TransferOwner` (trees move whole; clashing top-level names numbered), `MarkDeletedByOwner`, `ClearCreatedBy`, `ExpireUploadsBy`                                                                                                                                                                                                                               |
| `manager_owners.go`                                                                     | service-to-service lifecycle (iam calls these; never exposed to browsers): `DeleteOwned` (project/org/org-less user), `RemoveUser` (expire their uploads, transfer to org or trash, clear creator everywhere)                                                                                                                                                                                      |
| `store_owners_test.go`                                                                  | store-suite cases for the owner lifecycle, including ownership moving mid-upload                                                                                                                                                                                                                                                                                                                   |
| `store_trash_test.go`                                                                   | store-suite cases for `GetVersion`, the trash filter, restores and breadcrumbs                                                                                                                                                                                                                                                                                                                     |
| `postgres*.go`, `migrate.go`, `migrations/`, `queries.sql`, `sqlc.yaml`, `internal/db/` | the Postgres `Store` (schema `storagemanager`): runs the same shared suite as `MemoryStore` (`postgres_test.go`, needs `GYOZA_TEST_DATABASE_URL`). Timestamps come from Go; folder names unique via a partial unique index on `name_key` (`NULLS NOT DISTINCT`); per-owner advisory lock for tree changes; row locks for assets and upload sessions; `WITH RECURSIVE` for breadcrumbs and subtrees |
| `sniff.go`                                                                              | `checkSniffed`: do a file's first 512 bytes match its declared type? Rejects only clear mismatches (`ErrContentMismatch`)                                                                                                                                                                                                                                                                          |
| `store_test.go`                                                                         | shared `runStoreTests` suite (MemoryStore and Postgres)                                                                                                                                                                                                                                                                                                                                            |
| `purge.go`, `purge_memory.go`, `postgres_purge.go`                                      | `PurgeStore` (embedded in `Store`): trash listed by age, assets and folders purged for good, old completed sessions deleted                                                                                                                                                                                                                                                                        |
| `manager_cleanup.go`                                                                    | the cleanup job: `Cleanup` (expired uploads, completed sessions, trash, deleted ledger objects; files first, records last), `RunCleanup` (the timer), `restorable` (the restore cut-off)                                                                                                                                                                                                           |
| `service.go`                                                                            | `Service`: what the gateway and iam call; `*Manager` and `GRPCClient` implement it                                                                                                                                                                                                                                                                                                                 |
| `grpc.go`, `grpc_server.go`, `grpc_client.go`                                           | gRPC adapters: conversions, the `wireErrors` table (status code + ErrorInfo reason per sentinel), server and client                                                                                                                                                                                                                                                                                |
| `grpc_test.go`, `grpc_internal_test.go`                                                 | every call through a real client and server (bufconn) compared with the Manager directly; every error round-trips                                                                                                                                                                                                                                                                                  |
| `export_test.go`                                                                        | test-only hooks (`SetClock`, constants): in package `storagemanager` but compiled only into tests                                                                                                                                                                                                                                                                                                  |
| `assets_rules_test.go`                                                                  | internal unit tests of the unexported helpers                                                                                                                                                                                                                                                                                                                                                      |

Everything was mutation-checked: realistic bugs reintroduced one at a time are each caught by a named test (blobs 3/3, assets 9/9, folders 13/13, uploads 12/12, Manager steps 4-6 all caught, Postgres store 16/16, the upload rework 4/4, cleanup and purge 26/27, gRPC adapters 18/18). The one survivor is unobservable: MemoryStore keeping a purged asset's versions in its map. Run mutation checks with `go test -timeout 60s`: a bug that corrupts the folder tree can make walks loop forever.

## Todo: media metadata

Know an image/video/audio file's properties without fetching and inspecting its bytes, by extracting them once when it's stored.

- what: images: width, height, (color space, has alpha); video: width, height, duration, frame rate, bitrate, codecs, audio tracks; audio: duration, sample rate, channels, bitrate, codec. Maybe PDF page count, font family name later
- belongs to a **version**, not the asset: a replacement file can have a different resolution
- storage: typed columns for what we filter or sort by (width, height, duration), plus a JSON column for the rest, so new fields don't need migrations
- images: Go's `image.DecodeConfig` reads only the header (the first few KB, via `Blobs.ReadPrefix`), so it can run inline in `CompleteUpload`
- video/audio: `ffprobe` (media-processor) reads the container metadata over a presigned URL using HTTP range requests, without downloading the whole file. Too slow to run inline, so a job after `CompleteUpload` fills the metadata in; until then a version's metadata is "pending"
- open: whether exports (also media) get the same metadata, recorded by whoever writes them
