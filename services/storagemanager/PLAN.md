# Plan: storage-manager, phase 2 (asset refs, usage, forking, copying)

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
   `UnmarshalText` as "id@3"), proto message in `manager.proto`, tests; a store-suite
   case locking version immutability.
2. **`blob.Store.Copy(ctx, bucket, srcKey, dstKey)`** (`platform/blob`): S3 server-side
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
6. **gRPC + docs**: the new calls in `manager.proto`, adapters, round-trip tests; README
   (refs, uses, forking, trash behaviour); settle the policy for used assets whose owner
   is deleted (the ledger README's open "dangling references").

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

## Deployment change: one storage service (decided and done 2026-10-11)

storageledger and storagemanager stay separate packages and schemas, but deploy as one
storage service: the ledger only ever runs inside the storage binary, called
in-process.

- `cmd/storage-manager` (rename to `cmd/storage`) migrates and builds the ledger itself
  (`storageledger.NewPostgres`) and serves both `StorageManagerService` and
  `LedgerService` on its gRPC port (documentmanager records snapshots through the latter)
- remove `cmd/storage-ledger`, `GYOZA_LEDGER_ADDR` and the ledger gRPC client from the
  binary wiring (`storageledger.GRPCClient` stays, for other services' use and tests)
- docs: cmd README, CLAUDE.md, `.env.example`, both service READMEs
- why: storagemanager is the ledger's main writer and only purger; one process removes a
  network hop per completion and per cleanup item, and lets ledger and DAM writes share a
  transaction later if useful. The Go package boundary keeps the code separate.
