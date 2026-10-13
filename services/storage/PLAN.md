# Plan: storage

Phase 1 (DAM, cascades, Postgres store, S3, cleanup, gRPC), phase 2 (asset refs,
uses, forking, copying) and media metadata steps 1-3 are done; their record is the
README. Related: [iam/PLAN.md](../iam/PLAN.md), [documents/PLAN.md](../documents/PLAN.md)
(keeps uses, rewrites a clone's refs), [mediaprocessor/PLAN.md](../mediaprocessor/PLAN.md),
[apps/web/PLAN.md](../../apps/web/PLAN.md).

## Next: media metadata on versions

- expose media info on `Version` and in `storage.proto`, with gRPC round-trip tests and
  the README; `GetAsset` and version listings return dimensions/duration (or
  "pending") with each version
- a batch get on the ledger (`GetMediaInfo` for many objects), so listings don't make
  one call per version
- deferred: SVG size from `viewBox`; HEIC/AVIF through mediaprocessor (ffprobe reads
  them); EXIF orientation in PNG/WebP (rare); whether exports get media info

## Open from phase 2

- `go test -race` hasn't run (no C compiler on the dev machine)
- `blob.S3.Copy`'s `CopySource` escaping can only be proven against AWS S3 (RustFS
  accepts it either way): check when deploying
- deploy order once refs and clones are used: iam (with `AuthzService.ProjectOwner`)
  before storage
- `DeleteAsset` doesn't return the asset's use count yet (the trash listing does), so
  the UI can't warn before trashing a used file
- a reconciliation job, someday: compare S3 keys with the ledger and owners with iam,
  and report (not delete) mismatches. Not needed for the MVP; listing a big bucket
  isn't free (OPTIMIZATIONS.md when it's built)

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

## Document snapshots (done 2026-10-12, documents phase 3 step 2; record in the README)

Service to service (no gateway routes), presigned like uploads:
`BeginSnapshot(projectID, seq, editedBy, editedAt, contentType, size)` returns a PUT
ticket and records a pending row; `CompleteSnapshot(projectID, objectID)` checks the
object in S3, then in one transaction refuses a stale sequence (`ErrStale`), writes the
ledger row (`Kind=snapshot`, owner = the project, `CreatedBy` = the last editor) and
assigns the next version. `LatestSnapshot`, `SnapshotURL` (presigned GET). Metadata in a
new `storage.snapshots` table (migration 00005), package `internal/snapshots`.

- retention: `GYOZA_SNAPSHOT_KEEP` (default 100) newest per project; cleanup trashes
  older ones and the purge removes them. No daily stride.
- size cap: `GYOZA_SNAPSHOT_MAX_BYTES` (default 32 MiB): large projects caused I/O
  trouble near 100 MB before, and snapshots hold ECS tables and refs, not media.
- last edit, `touch`-style: each snapshot records who made the last change it holds
  and when (`edited_by`, `edited_at`), forwarded by documents' debouncer. Informational:
  not authorised, since the editor may have lost access since.
- cleanup also removes expired pending snapshots.
- `ListSnapshots(projectID, pageToken)`, newest first (version, seq, edited by/at), for
  administrative reverts: un-sticking a project by going back to an earlier version
  (documents/PLAN.md "Reverting"). The kept 100 are the revert window.
