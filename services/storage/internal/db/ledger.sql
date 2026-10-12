-- name: CreateObject :one
-- On an ID clash nothing is inserted and no row returned (pgx.ErrNoRows): the
-- caller then reads the existing object and compares (Create is idempotent).
INSERT INTO storage.objects (
    id, owner_kind, owner_id, created_by, project_id, kind,
    bucket, key, size_bytes, content_type
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10
)
ON CONFLICT (id) DO NOTHING
RETURNING *;

-- name: GetObject :one
SELECT * FROM storage.objects WHERE id = $1;

-- List queries: one per scope so each uses its own index. The scope and the
-- cursor are plain comparisons so they become index conditions: Postgres
-- jumps straight to the page and stops after row_limit rows. The first page
-- passes the zero cursor. Optional filters are NULL when unset and are
-- checked on the rows the index returns. Fetch one row more than the page
-- size to know whether there is a next page.

-- name: ListObjectsByOwner :many
SELECT * FROM storage.objects
WHERE owner_kind = sqlc.arg(owner_kind)
  AND owner_id = sqlc.arg(owner_id)
  AND (created_at, id) > (sqlc.arg(after_created_at)::timestamptz, sqlc.arg(after_id)::text)
  AND (sqlc.narg(created_by)::text IS NULL OR created_by = sqlc.narg(created_by))
  AND (sqlc.narg(kind)::text IS NULL OR kind = sqlc.narg(kind))
  AND (sqlc.arg(include_deleted)::bool OR deleted_at IS NULL)
ORDER BY created_at, id
LIMIT sqlc.arg(row_limit);

-- name: ListObjectsByProject :many
SELECT * FROM storage.objects
WHERE project_id = sqlc.arg(project_id)::text
  AND (created_at, id) > (sqlc.arg(after_created_at)::timestamptz, sqlc.arg(after_id)::text)
  AND (sqlc.narg(created_by)::text IS NULL OR created_by = sqlc.narg(created_by))
  AND (sqlc.narg(kind)::text IS NULL OR kind = sqlc.narg(kind))
  AND (sqlc.arg(include_deleted)::bool OR deleted_at IS NULL)
ORDER BY created_at, id
LIMIT sqlc.arg(row_limit);

-- name: MarkObjectDeleted :execrows
-- Keeps the original deletion time if already deleted; 0 rows means not found.
UPDATE storage.objects
SET deleted_at = COALESCE(deleted_at, now())
WHERE id = $1;

-- name: ClearCreatedBy :execrows
UPDATE storage.objects
SET created_by = NULL
WHERE created_by = sqlc.arg(user_id)::text;

-- name: TransferOwner :execrows
-- Both owners are checked in Go (never projects, never equal), so the
-- table's owner constraints hold after the update.
UPDATE storage.objects
SET owner_kind = sqlc.arg(to_kind)::text, owner_id = sqlc.arg(to_id)::text
WHERE owner_kind = sqlc.arg(from_kind)::text AND owner_id = sqlc.arg(from_id)::text;

-- name: MarkDeletedByOwner :execrows
UPDATE storage.objects
SET deleted_at = now()
WHERE owner_kind = sqlc.arg(owner_kind)::text AND owner_id = sqlc.arg(owner_id)::text
  AND deleted_at IS NULL;

-- name: ListDeletedObjects :many
-- Uses the partial index objects_deleted (migration 00003).
SELECT * FROM storage.objects
WHERE deleted_at IS NOT NULL AND deleted_at < sqlc.arg(before)::timestamptz
ORDER BY deleted_at, id
LIMIT sqlc.arg(row_limit);

-- name: PurgeObject :execrows
-- Only deleted objects; 0 rows means missing or live (the caller tells which).
DELETE FROM storage.objects WHERE id = $1 AND deleted_at IS NOT NULL;

-- name: MarkMediaPending :execrows
-- Queues an existing object for probing; an object that already has a row
-- keeps it. 0 rows means the object is missing or already has one (the
-- caller tells which).
INSERT INTO storage.media_info (object_id, status, updated_at)
SELECT id, 'pending', clock_timestamp() FROM storage.objects WHERE id = $1
ON CONFLICT (object_id) DO NOTHING;

-- name: SetMediaInfo :execrows
-- Records a probe's outcome, replacing what was there. 0 rows means the
-- object is missing.
INSERT INTO storage.media_info (object_id, status, width, height, duration_us, details, error, attempts, updated_at)
SELECT id, sqlc.arg(status)::text, sqlc.arg(width)::int, sqlc.arg(height)::int,
       sqlc.arg(duration_us)::bigint, sqlc.arg(details)::jsonb, sqlc.arg(error)::text,
       sqlc.arg(attempts)::int, clock_timestamp()
FROM storage.objects WHERE id = sqlc.arg(object_id)::text
ON CONFLICT (object_id) DO UPDATE SET
    status = EXCLUDED.status, width = EXCLUDED.width, height = EXCLUDED.height,
    duration_us = EXCLUDED.duration_us, details = EXCLUDED.details, error = EXCLUDED.error,
    attempts = EXCLUDED.attempts, updated_at = EXCLUDED.updated_at;

-- name: GetMediaInfo :one
SELECT * FROM storage.media_info WHERE object_id = $1;

-- name: ListPendingMedia :many
-- Uses the partial index media_info_pending. Deleted objects are skipped:
-- nobody will look at their metadata, and the purge drops their rows.
SELECT m.* FROM storage.media_info m
JOIN storage.objects o ON o.id = m.object_id
WHERE m.status = 'pending' AND o.deleted_at IS NULL
ORDER BY m.updated_at, m.object_id
LIMIT sqlc.arg(row_limit);
