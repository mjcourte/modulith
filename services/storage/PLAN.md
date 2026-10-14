# Plan: storage (`services/storage`)

What's built is in [README.md](README.md). Next: what the web app's file browser needs
(planned 2026-10-14, [apps/web/PLAN.md](../../apps/web/PLAN.md) "Part 2"). Gateway
routes for each step are listed with it ([gateway/PLAN.md](../gateway/PLAN.md) has the
conventions).

## Step 1: project shelf (link/unlink)

A library asset pinned to a project without being in its document.

- **The conflict:** `asset_uses` rows are `(project, asset, version)`. Documents'
  `syncUses` (`services/documents/internal/manager/uses.go`) forgets a ref when it
  leaves the document, so it would also drop a shelf link on the same version.
- **Migration** (forward): add `source` (`'document'` | `'link'`) to the key; existing
  rows become `'document'`. `RecordAssetUse`/`ForgetAssetUse` keep their signatures and
  touch `'document'` rows only.
- **`LinkAsset`/`UnlinkAsset(actor, project, ref)`** act on `'link'` rows, with the same
  checks as recording: write on the project, read on the asset, rule A
  (`ErrForkRequired`). **`ListProjectAssets(actor, project)`** returns the uses with
  their source; it needs read on the project.
- Anything that counts uses (`ResolveRef`, `ListTrash`'s count, the purge cascade)
  counts both sources.
- Changes: the `UseStore` (`internal/dam/uses*.go`, `postgres_uses.go`, `manager.sql`),
  `manager_uses.go`, proto, grpcapi, `storage.go`.
- Gateway: `GET/POST /projects/{id}/assets`, `DELETE /projects/{id}/assets/{assetId}/{version}`.

## Step 2: read-only shares and copies

- **Grants:** `storage.asset_shares(asset_id, user_id, granted_by, created_at)` and
  `storage.folder_shares(folder_id, ...)`, `ON DELETE CASCADE` from the asset or folder.
  They live in storage, which owns the data.
- **Read rule:** the owner rules as today, *or* a grant on the asset or any ancestor
  folder (`WITH RECURSIVE`, like `FolderPath`). Grants allow read and download only,
  never write. Using a shared asset in a project falls under rule A, so it needs a fork
  (`ForkAsset` already allows a direct read).
- **Who may share:** anyone with write on the item.
- **Calls:** `ShareAsset`/`UnshareAsset`/`ListAssetShares`, the same for folders, and
  `ListSharedWithMe(actor, page)`. `ListFolderContents` inside a shared folder goes
  through the grant check. List and folder results gain a `shared` flag (or `access`)
  for the UI's badges.
- **Editing = copying** (decided 2026-10-14; edit grants maybe later):
  `CopyAsset(actor, ref, target owner, folder)` copies one version into a new asset.
  It uses the same resumable steps as forks (ledger object, `blob.Store.Copy`, media
  info, asset last) and needs read on the source and write on the target. Works for
  anything readable (own, org, project, shared). The copy belongs to the copier
  entirely. `CopyFolder` (recursive: folders first, then each asset as above) may be its
  own step.
- **GDPR:** `RemoveUser` deletes the user's grants; `DeleteOwned` cascades through the
  rows.
- **Email → user:** the UI shares by email; the gateway resolves it through iam. Check
  for an existing lookup first, else add a narrow `FindUserByEmail` (existing users only;
  an unknown email is a 404 "they must sign in first"; no pending grants in the MVP).
- Gateway: `GET/POST /assets/{id}/shares`, `DELETE /assets/{id}/shares/{userId}`, the
  same under `/folders/{id}`, `GET /shared`, `POST /assets/{id}/copy`
  (`{version, owner, folderId}`), and entries in the error table.

## Tests

- Shared store suite cases for sources and grants (MemoryStore and Postgres).
- Manager tests on the real rules:
  - Shelf: a link survives the document forgetting the same ref; unlink leaves document
    uses alone.
  - Grantee access: the grantee reads and downloads, but can't write; folder grants are
    inherited; revoking removes access; trashed items aren't served; the copy is owned
    by the target.
- `manager_access_test.go` pins the new routes. The gRPC suite over bufconn. Gateway
  harness flows, plus a stranger's 403.
- Mutation-check the read rule, inheritance and the source filter.
