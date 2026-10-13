-- Document snapshots (internal/snapshots/postgres.go).

-- name: InsertPendingSnapshot :exec
INSERT INTO storage.snapshots (object_id, project_id, seq, edited_by, edited_at, bucket, key, content_type, size_bytes, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- name: GetSnapshot :one
SELECT * FROM storage.snapshots WHERE object_id = $1;

-- name: LatestSnapshot :one
SELECT * FROM storage.snapshots
WHERE project_id = $1 AND version IS NOT NULL
ORDER BY version DESC
LIMIT 1;

-- name: LockProjectSnapshots :exec
-- Serialises completions per project for the rest of the transaction, even
-- for a project with no rows yet to lock.
SELECT pg_advisory_xact_lock(hashtextextended('storage.snapshots:' || @project_id::text, 0));

-- name: CompleteSnapshot :one
UPDATE storage.snapshots
SET version = @version, completed_at = now()
WHERE object_id = @object_id AND version IS NULL
RETURNING *;

-- name: ListSnapshots :many
-- Newest first, before a version (0: from the newest).
SELECT * FROM storage.snapshots
WHERE project_id = @project_id AND version IS NOT NULL
  AND (@before::int = 0 OR version < @before::int)
ORDER BY version DESC
LIMIT @lim;

-- name: ListExpiredPendingSnapshots :many
SELECT * FROM storage.snapshots
WHERE version IS NULL AND expires_at < @before
ORDER BY expires_at
LIMIT @lim;

-- name: ListSnapshotsBeyondKeep :many
-- Completed snapshots older than each project's newest @keep, oldest first.
SELECT s.* FROM storage.snapshots s
JOIN (
    SELECT object_id, row_number() OVER (PARTITION BY project_id ORDER BY version DESC) AS rank
    FROM storage.snapshots
    WHERE version IS NOT NULL
) r ON r.object_id = s.object_id
WHERE r.rank > @keep::int
ORDER BY s.project_id, s.version
LIMIT @lim;

-- name: DeleteSnapshot :exec
DELETE FROM storage.snapshots WHERE object_id = $1;

-- name: DeleteProjectSnapshots :execrows
DELETE FROM storage.snapshots WHERE project_id = $1;

-- name: ClearSnapshotEditor :execrows
UPDATE storage.snapshots SET edited_by = NULL WHERE edited_by = $1;
