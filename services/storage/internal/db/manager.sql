-- ======== Assets and versions

-- name: InsertAsset :exec
INSERT INTO storage.assets (
    id, owner_kind, owner_id, project_id, folder_id, name, description, tags,
    content_type, category, current_version, created_by, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14
);

-- name: InsertVersion :exec
INSERT INTO storage.asset_versions (asset_id, number, object_id, content_type, size_bytes, created_by, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: GetAsset :one
SELECT * FROM storage.assets WHERE id = $1;

-- name: GetAssetForUpdate :one
-- Locks the row for the rest of the transaction, so concurrent writes to one
-- asset (e.g. two new versions) take turns.
SELECT * FROM storage.assets WHERE id = $1 FOR UPDATE;

-- name: SaveAssetFields :exec
UPDATE storage.assets
SET name = $2, description = $3, tags = $4, folder_id = $5, updated_at = $6
WHERE id = $1;

-- name: SetCurrentVersion :exec
UPDATE storage.assets
SET current_version = $2, content_type = $3, updated_at = $4
WHERE id = $1;

-- name: CountVersions :one
SELECT count(*) FROM storage.asset_versions WHERE asset_id = $1;

-- name: ListVersions :many
SELECT * FROM storage.asset_versions WHERE asset_id = $1 ORDER BY number;

-- name: GetVersion :one
SELECT * FROM storage.asset_versions WHERE asset_id = $1 AND number = $2;

-- name: MarkAssetDeleted :execrows
-- Keeps the first deletion time; 0 rows means no such asset.
UPDATE storage.assets SET deleted_at = COALESCE(deleted_at, sqlc.arg(at)) WHERE id = sqlc.arg(id);

-- name: RestoreAsset :exec
UPDATE storage.assets
SET deleted_at = NULL, deletion_id = NULL, folder_id = $2, updated_at = $3
WHERE id = $1;

-- Listings: one query per scope so each uses its own index. The cursor is a
-- plain comparison (the first page passes the zero UUID) so it's an index
-- condition; optional filters are '' / false when unset.

-- name: ListAssetsByOwner :many
SELECT * FROM storage.assets
WHERE owner_kind = sqlc.arg(owner_kind) AND owner_id = sqlc.arg(owner_id)
  AND id > sqlc.arg(after_id)::uuid
  AND (sqlc.arg(tag)::text = '' OR tags @> ARRAY[sqlc.arg(tag)::text])
  AND (sqlc.arg(category)::text = '' OR category = sqlc.arg(category)::text)
  AND (NOT sqlc.arg(filter_folder)::bool OR folder_id IS NOT DISTINCT FROM sqlc.narg(folder_id)::uuid)
  AND (sqlc.arg(include_deleted)::bool OR deleted_at IS NULL)
  AND (NOT sqlc.arg(only_deleted)::bool OR deleted_at IS NOT NULL)
ORDER BY id
LIMIT sqlc.arg(row_limit);

-- Folder listings (owner scope): the folder condition is a plain = / IS NULL,
-- so assets_folder_page serves them; IS NOT DISTINCT FROM would not.

-- name: ListAssetsInFolder :many
SELECT * FROM storage.assets
WHERE owner_kind = sqlc.arg(owner_kind) AND owner_id = sqlc.arg(owner_id)
  AND folder_id = sqlc.arg(folder_id)::uuid
  AND id > sqlc.arg(after_id)::uuid
  AND (sqlc.arg(tag)::text = '' OR tags @> ARRAY[sqlc.arg(tag)::text])
  AND (sqlc.arg(category)::text = '' OR category = sqlc.arg(category)::text)
  AND (sqlc.arg(include_deleted)::bool OR deleted_at IS NULL)
  AND (NOT sqlc.arg(only_deleted)::bool OR deleted_at IS NOT NULL)
ORDER BY id
LIMIT sqlc.arg(row_limit);

-- name: ListAssetsAtTop :many
SELECT * FROM storage.assets
WHERE owner_kind = sqlc.arg(owner_kind) AND owner_id = sqlc.arg(owner_id)
  AND folder_id IS NULL
  AND id > sqlc.arg(after_id)::uuid
  AND (sqlc.arg(tag)::text = '' OR tags @> ARRAY[sqlc.arg(tag)::text])
  AND (sqlc.arg(category)::text = '' OR category = sqlc.arg(category)::text)
  AND (sqlc.arg(include_deleted)::bool OR deleted_at IS NULL)
  AND (NOT sqlc.arg(only_deleted)::bool OR deleted_at IS NOT NULL)
ORDER BY id
LIMIT sqlc.arg(row_limit);

-- name: ListAssetsByProject :many
SELECT * FROM storage.assets
WHERE project_id = sqlc.arg(project_id)::text
  AND id > sqlc.arg(after_id)::uuid
  AND (sqlc.arg(tag)::text = '' OR tags @> ARRAY[sqlc.arg(tag)::text])
  AND (sqlc.arg(category)::text = '' OR category = sqlc.arg(category)::text)
  AND (NOT sqlc.arg(filter_folder)::bool OR folder_id IS NOT DISTINCT FROM sqlc.narg(folder_id)::uuid)
  AND (sqlc.arg(include_deleted)::bool OR deleted_at IS NULL)
  AND (NOT sqlc.arg(only_deleted)::bool OR deleted_at IS NOT NULL)
ORDER BY id
LIMIT sqlc.arg(row_limit);

-- ======== Folders

-- name: LockOwnerTree :exec
-- Serialises changes to one owner's folder tree for the rest of the
-- transaction, so concurrent moves can't combine into a cycle.
SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg(owner_key)::text, 0));

-- name: InsertFolder :exec
INSERT INTO storage.folders (id, owner_kind, owner_id, parent_id, name, name_key, created_by, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: GetFolder :one
SELECT * FROM storage.folders WHERE id = $1;

-- name: ListTopFolders :many
SELECT * FROM storage.folders
WHERE owner_kind = $1 AND owner_id = $2 AND parent_id IS NULL AND deleted_at IS NULL
ORDER BY name_key;

-- name: ListChildFolders :many
SELECT * FROM storage.folders
WHERE owner_kind = $1 AND owner_id = $2 AND parent_id = $3 AND deleted_at IS NULL
ORDER BY name_key;

-- name: FolderNameTaken :one
SELECT EXISTS (
    SELECT 1 FROM storage.folders
    WHERE owner_kind = sqlc.arg(owner_kind) AND owner_id = sqlc.arg(owner_id)
      AND parent_id IS NOT DISTINCT FROM sqlc.narg(parent_id)::uuid
      AND name_key = sqlc.arg(name_key) AND deleted_at IS NULL
      AND id <> sqlc.arg(except_id)::uuid
);

-- name: FolderPath :many
-- The folder and its ancestors, top level first. The depth bound stops a
-- corrupted (cyclic) tree from looping forever.
WITH RECURSIVE up AS (
    SELECT f.id, f.parent_id, 1 AS depth FROM storage.folders f WHERE f.id = sqlc.arg(id)::uuid
    UNION ALL
    SELECT p.id, p.parent_id, up.depth + 1
    FROM storage.folders p JOIN up ON p.id = up.parent_id
    WHERE up.depth < 64
)
SELECT f.* FROM up JOIN storage.folders f ON f.id = up.id ORDER BY up.depth DESC;

-- name: LiveSubtree :many
-- The folder and its live descendants with their level (1 = the folder).
WITH RECURSIVE down AS (
    SELECT f.id, 1 AS level FROM storage.folders f WHERE f.id = sqlc.arg(id)::uuid
    UNION ALL
    SELECT c.id, down.level + 1
    FROM storage.folders c JOIN down ON c.parent_id = down.id
    WHERE c.deleted_at IS NULL AND down.level < 64
)
SELECT id, level FROM down;

-- name: RenameFolder :exec
UPDATE storage.folders SET name = $2, name_key = $3, updated_at = $4 WHERE id = $1;

-- name: MoveFolder :exec
UPDATE storage.folders SET parent_id = $2, updated_at = $3 WHERE id = $1;

-- name: CountLiveAssetsIn :one
SELECT count(*) FROM storage.assets
WHERE folder_id = ANY (sqlc.arg(folder_ids)::uuid[]) AND deleted_at IS NULL;

-- name: TrashFolders :execrows
UPDATE storage.folders SET deleted_at = sqlc.arg(at), deletion_id = sqlc.arg(deletion_id)::uuid
WHERE id = ANY (sqlc.arg(ids)::uuid[]) AND deleted_at IS NULL;

-- name: TrashAssetsIn :execrows
UPDATE storage.assets SET deleted_at = sqlc.arg(at), deletion_id = sqlc.arg(deletion_id)::uuid
WHERE folder_id = ANY (sqlc.arg(folder_ids)::uuid[]) AND deleted_at IS NULL;

-- name: ListTrashedFolders :many
SELECT * FROM storage.folders
WHERE owner_kind = $1 AND owner_id = $2 AND deleted_at IS NOT NULL
ORDER BY deleted_at DESC, id;

-- name: FoldersInDeletion :many
SELECT * FROM storage.folders WHERE deletion_id = $1 AND deleted_at IS NOT NULL;

-- name: AssetsInDeletion :many
SELECT * FROM storage.assets WHERE deletion_id = $1 AND deleted_at IS NOT NULL;

-- name: RestoreFolder :exec
UPDATE storage.folders
SET parent_id = $2, name = $3, name_key = $4, deleted_at = NULL, deletion_id = NULL, updated_at = $5
WHERE id = $1;

-- ======== Upload sessions

-- name: InsertUpload :exec
INSERT INTO storage.uploads (
    id, owner_kind, owner_id, project_id, created_by, asset_id, name, description, tags, folder_id,
    bucket, key, content_type, size_bytes, expires_at, created_at, object_id
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17
);

-- name: GetUpload :one
SELECT * FROM storage.uploads WHERE id = $1;

-- name: GetUploadForUpdate :one
-- Locks the session, so overlapping completions take turns.
SELECT * FROM storage.uploads WHERE id = $1 FOR UPDATE;

-- name: MarkUploadCompleted :exec
UPDATE storage.uploads SET completed_at = $2, result_asset_id = $3, result_version = $4 WHERE id = $1;

-- name: ListExpiredUploads :many
SELECT * FROM storage.uploads
WHERE completed_at IS NULL AND expires_at < sqlc.arg(before)
ORDER BY expires_at, id
LIMIT sqlc.arg(row_limit);

-- name: DeleteUpload :exec
DELETE FROM storage.uploads WHERE id = $1;

-- name: ExpireUploadsBy :execrows
UPDATE storage.uploads SET expires_at = sqlc.arg(at)
WHERE created_by = sqlc.arg(user_id) AND completed_at IS NULL AND expires_at > sqlc.arg(at);

-- ======== Owner lifecycle

-- name: TransferFolder :exec
-- One top-level folder, with its (possibly renamed) name, so it never clashes
-- with the new owner's folders or with siblings still waiting to move.
UPDATE storage.folders SET owner_kind = $2, owner_id = $3, name = $4, name_key = $5 WHERE id = $1;

-- name: TransferFolders :execrows
UPDATE storage.folders SET owner_kind = sqlc.arg(to_kind), owner_id = sqlc.arg(to_id)
WHERE owner_kind = sqlc.arg(from_kind) AND owner_id = sqlc.arg(from_id);

-- name: TransferAssets :execrows
UPDATE storage.assets SET owner_kind = sqlc.arg(to_kind), owner_id = sqlc.arg(to_id)
WHERE owner_kind = sqlc.arg(from_kind) AND owner_id = sqlc.arg(from_id);

-- name: TrashOwnerFolders :execrows
UPDATE storage.folders SET deleted_at = sqlc.arg(at), deletion_id = sqlc.arg(deletion_id)::uuid
WHERE owner_kind = sqlc.arg(owner_kind) AND owner_id = sqlc.arg(owner_id) AND deleted_at IS NULL;

-- name: TrashOwnerAssets :execrows
UPDATE storage.assets SET deleted_at = sqlc.arg(at), deletion_id = sqlc.arg(deletion_id)::uuid
WHERE owner_kind = sqlc.arg(owner_kind) AND owner_id = sqlc.arg(owner_id) AND deleted_at IS NULL;

-- name: ClearAssetCreator :execrows
UPDATE storage.assets SET created_by = '' WHERE created_by = $1;

-- name: ClearFolderCreator :execrows
UPDATE storage.folders SET created_by = '' WHERE created_by = $1;

-- name: ClearVersionCreator :execrows
UPDATE storage.asset_versions SET created_by = '' WHERE created_by = $1;

-- ======== Purge (the cleanup job)
-- The listings use the partial indexes of migration 00003, which hold only
-- trashed or completed rows.

-- name: ListAssetsTrashedBefore :many
SELECT * FROM storage.assets
WHERE deleted_at IS NOT NULL AND deleted_at < sqlc.arg(before)
ORDER BY deleted_at, id
LIMIT sqlc.arg(row_limit);

-- name: DeleteAssetVersions :exec
DELETE FROM storage.asset_versions WHERE asset_id = $1;

-- name: DeleteAsset :exec
DELETE FROM storage.assets WHERE id = $1;

-- name: ListFoldersTrashedBefore :many
-- Locks the rows, so two cleanup runs don't purge the same folders at once.
SELECT id FROM storage.folders
WHERE deleted_at IS NOT NULL AND deleted_at < sqlc.arg(before)
ORDER BY deleted_at, id
LIMIT sqlc.arg(row_limit)
FOR UPDATE;

-- name: DetachFoldersFromParents :exec
-- Trashed folders inside the purged ones move to the top level. Live ones
-- are left alone, so the foreign key refuses the purge instead.
UPDATE storage.folders SET parent_id = NULL
WHERE parent_id = ANY(sqlc.arg(ids)::uuid[]) AND deleted_at IS NOT NULL
  AND NOT id = ANY(sqlc.arg(ids)::uuid[]);

-- name: DetachAssetsFromFolders :exec
UPDATE storage.assets SET folder_id = NULL
WHERE folder_id = ANY(sqlc.arg(ids)::uuid[]) AND deleted_at IS NOT NULL;

-- name: DeleteFolders :exec
-- One statement, so a parent and its child in the same batch can go
-- together: foreign keys are checked at the end of the statement.
DELETE FROM storage.folders WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: DeleteCompletedUploads :execrows
DELETE FROM storage.uploads WHERE id IN (
    SELECT old.id FROM storage.uploads old
    WHERE old.completed_at IS NOT NULL AND old.completed_at < sqlc.arg(before)
    ORDER BY old.completed_at, old.id
    LIMIT sqlc.arg(row_limit)
);
