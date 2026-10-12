# Plan: storage, phase 2 (asset refs, usage, forking, copying)

Phase 1 (steps 1-10: DAM, cascades, Postgres store, S3, cleanup, gRPC) is done; its
record is the README. This phase serves cloning and forking, decided while planning
iam (2026-10-11). Related: [iam/PLAN.md](../iam/PLAN.md)
(`CloneProject` calls this), [documentmanager/PLAN.md](../documentmanager/PLAN.md)
(keeps usage rows, rewrites refs), [apps/web/PLAN.md](../../apps/web/PLAN.md).

## Decisions (agreed 2026-10-11)

- **Asset refs are concrete and always pinned**: documents point at
  `AssetRef{AssetID, Version}` (Version >= 1). APIs may take version 0 for "current",
  resolved immediately into a pinned ref. New uploads and reverts never move existing
  refs; "update available" is `ref.Version < asset.CurrentVersion`.
- **Versions are immutable**: a version's `ObjectID`, `ContentType` and `SizeBytes`
  never change (only `CreatedBy` can be cleared, for GDPR); name, description, tags and
  folder live on the asset. So a ref always means the same bytes: reproducible
  rendering, trivially cacheable.
- **Asset uses are recorded**: a usage table `(project, asset, version)`, kept by
  documentmanager as refs are added and removed. Live projects reference library assets
  (their owners' later uploads don't move pinned refs); clones get frozen copies.
- **Reading through a project**: reading a project grants read on the refs it uses,
  checked "via the project": allowed if the project uses the ref and the actor can read
  the project (one usage lookup + one iam check).
- **Trashed but used**: a project keeps reading refs it uses while their asset is in
  the trash, until purged; trashing only hides it from its owner's library. Results
  carry `DeletedAt` so the UI marks it; after the purge the ref resolves to not found.
  Trashing an asset that projects use reports the usage count so the UI can warn.
- **Forking shared assets**: nobody modifies, versions or reverts an asset they can read
  but not write (a gyoza library asset, someone else's). Editing it forks it: a distinct
  copy of the referenced version (version 1 of a new asset, file copied server-side, its
  own ledger object), owned by the project it's used in (default) or the user's library,
  and that project's usage row moves to the copy. Others using the original are
  untouched; the original's owner can still change or delete it. Explicit: writes to an
  asset the actor can't write still return `ErrForbidden`; the client calls `ForkAsset`.
- **Copies never share files**: every copy (clone or fork) gets its own S3 key and
  ledger object, because the purge deletes a file with its object (one ledger object
  per key). Copies get `CreatedBy` = the actor.
- **Deterministic copy IDs**: copy ID = UUIDv5(target, source asset, version), so a
  retried copy lands on the same asset and documentmanager can compute rewritten refs
  (`{copy ID, 1}`) without a mapping table.

## What a clone copies (`CopyProjectFiles`)

Into the clone project, owned by it: the folder tree; each live project-owned asset's
current version; and every version the source's usage rows reference, whoever owns it.
Each copied version becomes its own asset at version 1 (no history). The clone is then
independent of the source in both directions: the source can change, trash or purge
anything, and the cloner can edit freely.

## Steps

1. **`AssetRef`**: type next to `Asset`/`Version` (`Validate`, `String`, `MarshalText`/
   `UnmarshalText` as "id@3"), proto message in `storage.proto`, tests; a store-suite
   case locking version immutability.
2. **`blob.Store.Copy(ctx, bucket, srcKey, dstKey)`** (`internal/blob`): S3 server-side
   CopyObject (no bytes through our servers; single copy up to 5 GiB, matching the
   upload limit), memory fake, a `blobtest` case run on RustFS too.
3. **Asset uses**: `asset_uses` table (migration), Store methods (record, forget, list by
   project, count by asset; idempotent), `Service` calls for documentmanager (service to
   service only), rows removed when a project or asset is deleted/purged; shared suite,
   mutation check. Decide then whether `Asset.ProjectID` stays as "created for" or goes.
4. **`ResolveRef(actor, projectID, ref)`**: the "via a project" read path (works for
   trashed assets until purged); returns the asset (with `DeletedAt`), the version and a
   download URL. Trash results report usage counts.
5. **Copying with chosen IDs**: Store creates an asset with a given ID (idempotent);
   `CopyProjectFiles(from, to)` on `Service` (service to service, resumable);
   `ForkAsset(actor, ref, projectID, target)` (read on the asset, write on the target;
   copy + usage swap in one transaction), sharing the copy machinery.
6. **gRPC + docs**: the new calls in `storage.proto`, adapters, round-trip tests; README
   (refs, uses, forking, trash behaviour); settle the policy for used assets whose owner
   is deleted (the README's open "dangling references").

## Verification

Shared store suite on MemoryStore and Postgres, blob suite on RustFS, mutation check per
step, `go test -race`.

## Later: stored avatars (decided 2026-10-11, not scheduled)

For now `iam.User.ProviderAvatarURL` is Google's picture URL, refreshed at each
sign-in. When users can upload their own avatar (or org logos), store them ourselves:

- a ledger-only object of a new `KindAvatar` (not a DAM asset: nobody names, tags or
  versions it), owned by the user (or org), key `library/avatar/{id}`
- personal kinds are never transferred: `RemoveUser` currently moves everything the user
  owns to their org (`TransferOwner`), so avatars must be excluded there and deleted
  instead (e.g. an "except these kinds" option on the ledger's `TransferOwner`)
- the user row stores the object ID, not a URL; avatars need longer-lived or public
  cacheable URLs, since short-lived presigned URLs defeat browser image caching

## Later: document snapshots through storage (decided 2026-10-11, not scheduled)

Storage is one service with one API (`StorageService`); the ledger has no API of its
own. documentmanager doesn't write to S3 itself: it calls storage over gRPC for its
snapshot S3 operations, like every other service. To design when documentmanager
needs them:

- `StorageService` calls for snapshots (write/presign, read, list a project's, delete),
  service to service only; storage builds the keys (`projects/{id}/snapshot/{id}`) and
  records each snapshot as a ledger object (`Kind=snapshot`)
- whether bytes go through storage or through presigned URLs (log-rolled snapshots may
  be large and frequent)

## Next: media metadata (decided 2026-10-12, not scheduled)

Built parts are recorded in the README ("Media metadata"). Know a stored file's dimensions,
duration, codecs, ... without fetching it, by probing it once when it's stored.
Related: [mediaprocessor/PLAN.md](../mediaprocessor/PLAN.md) (does the probing).

### Decisions

- **Keyed by ledger object, owned by the ledger**: a `storage.media_info` table with
  `object_id` as primary key (FK to `storage.objects`). A version points at exactly one
  object, so this is per version, as the README wants; it also covers exports and any
  other ledger object, settling the README's open question. The read/write calls go on
  `ledger.Service` (memory + Postgres, shared suite), since the ledger is where callers
  look an object up. `objects` itself stays narrow (it's the GDPR index; most rows
  aren't media).
- **Typed columns for what we filter or sort by, JSON for the rest**:
  migration `00002_media_info.sql`: `width`/`height` (display size, after rotation),
  `duration_us`, all 0 = unknown, plus `details` jsonb (`mediaprocessor.Info`), `error`,
  `attempts`, `updated_at` (`clock_timestamp()`, so it advances within a transaction).
- **Images inline, video/audio by a job**: raster images are measured in `CompleteUpload`
  (the same `ReadPrefix` as sniffing, 256 KiB for images since JPEG metadata can push
  the size back; `image.DecodeConfig` for JPEG/PNG/GIF/WebP; EXIF orientation 5-8 swaps
  to the display size) and written `ready`, or `failed` with why (HEIC/AVIF, corrupt):
  the upload succeeds either way. SVG is its own DAM category, `vector`, with no media
  info. Video/audio are marked `pending` right after the ledger object
  is created. Not one transaction (the ledger and the DAM store are separate), but
  `MarkMediaPending` is idempotent and runs before the session completes, so a retried
  `CompleteUpload` queues it again.
- **Storage calls mediaprocessor, never the reverse**: a background loop in storage
  (like `RunCleanup`; no NATS for the MVP) takes pending rows, presigns a short-lived
  GET, calls `mediaprocessor.Prober.Probe(ctx, url)` (in-process in the monolith, gRPC
  when distributed) and writes the result. mediaprocessor gets a URL only: no S3
  credentials, no Postgres. Failures bump
  `attempts`; after N they become `failed` with the error kept.
- **Purge**: purging an object deletes its `media_info` row (`ON DELETE CASCADE`).
- **Reads**: `GetAsset` and version listings join `media_info` so the frontend gets
  dimensions/duration (or "pending") with the version.

### Steps

1. Done (2026-10-12): migration + ledger calls (`MarkMediaPending`, `SetMediaInfo`,
   `GetMediaInfo`, `ListPendingMedia`), shared suite on Memory and Postgres, purge
   removes the row; mutation-checked (3/3). Still to do: a batch get for listings (with
   step 4).
2. Done (2026-10-12): `CategoryVector` (migration 00003 moves existing SVGs);
   `measureImage` / `jpegOrientation` / `recordImageInfo` (`manager_image.go`), table and
   malformed-EXIF tests plus `FuzzJPEGOrientation` (`manager_image_internal_test.go`),
   upload tests (`manager_image_test.go`); mutation-checked (4 + 4). `Info` got JSON tags,
   so all `details` are camelCase. Deferred: SVG size from `viewBox`; HEIC/AVIF via
   mediaprocessor (ffprobe reads them); EXIF orientation in PNG/WebP (rare).
3. Done (2026-10-12): video/audio queued in `CompleteUpload`; `ProbeMedia` /
   `RunMediaProbe` (`manager_media.go`) with a fake prober in tests; 3 attempts then
   `failed`, shutdown doesn't use an attempt; mutation-checked (5/5).
   `StartMediaProbe` in `wiring.go`, `GYOZA_PROBE_INTERVAL` (default 10s). Runs in
   `cmd/gyoza` only, and is off if ffprobe isn't on PATH; `cmd/storage` waits for
   mediaprocessor's gRPC API. Verified end to end against RustFS + Postgres.
4. Expose on `Version` / `storage.proto`, gRPC round-trip tests; README.
