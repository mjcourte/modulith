-- name: InsertProject :exec
INSERT INTO iam.projects (
    id, owner_kind, owner_id, name, state, cloned_from, org_locked, created_by, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9);

-- name: GetProject :one
SELECT * FROM iam.projects WHERE id = $1;

-- name: GetProjectForUpdate :one
SELECT * FROM iam.projects WHERE id = $1 FOR UPDATE;

-- name: GetProjectForShare :one
SELECT * FROM iam.projects WHERE id = $1 FOR SHARE;

-- name: SaveProject :exec
UPDATE iam.projects
SET owner_kind = $2, owner_id = $3, name = $4, state = $5, org_locked = $6, updated_at = $7
WHERE id = $1;

-- name: ListProjectsByOwner :many
SELECT * FROM iam.projects
WHERE owner_kind = $1 AND owner_id = $2 AND state = 'ready' AND deleting_at IS NULL
  AND id > sqlc.arg(after)::uuid
ORDER BY id
LIMIT sqlc.arg(lim);

-- name: ListVisibleProjects :many
-- A user's projects: their own, their org's (an empty org_id, for none,
-- matches nothing: org projects always have an ID), and
-- those shared with them while the share counts (directory.shareActive). One
-- branch per route, each a page from its own index (projects_owner_page,
-- project_shares_user), merged: an OR over the routes scans every project
-- (OPTIMIZATIONS.md). The shares branch skips projects the other two
-- already list; what it keeps isn't the user's org's, so its share counts
-- unless the project is locked.
SELECT v.* FROM (
    (SELECT p.* FROM iam.projects p
     WHERE p.owner_kind = 'user' AND p.owner_id = sqlc.arg(user_id)::text
       AND p.id > sqlc.arg(after)::uuid AND p.state = 'ready' AND p.deleting_at IS NULL
     ORDER BY p.id LIMIT sqlc.arg(lim))
    UNION ALL
    (SELECT p.* FROM iam.projects p
     WHERE p.owner_kind = 'org' AND p.owner_id = sqlc.arg(org_id)::text
       AND p.id > sqlc.arg(after)::uuid AND p.state = 'ready' AND p.deleting_at IS NULL
     ORDER BY p.id LIMIT sqlc.arg(lim))
    UNION ALL
    (SELECT p.* FROM iam.project_shares s JOIN iam.projects p ON p.id = s.project_id
     WHERE s.user_id = sqlc.arg(user_id)::text AND s.project_id > sqlc.arg(after)::uuid
       AND p.state = 'ready' AND p.deleting_at IS NULL AND NOT p.org_locked
       AND NOT (p.owner_kind = 'user' AND p.owner_id = sqlc.arg(user_id)::text)
       AND NOT (p.owner_kind = 'org' AND p.owner_id = sqlc.arg(org_id)::text)
     ORDER BY s.project_id LIMIT sqlc.arg(lim))
) v
ORDER BY v.id
LIMIT sqlc.arg(lim);

-- name: GetShare :one
SELECT * FROM iam.project_shares WHERE project_id = $1 AND user_id = $2;

-- name: InsertShare :exec
INSERT INTO iam.project_shares (project_id, user_id, access, shared_by, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $5);

-- name: UpdateShare :exec
UPDATE iam.project_shares SET access = $3, shared_by = $4, updated_at = $5
WHERE project_id = $1 AND user_id = $2;

-- name: ListShares :many
SELECT * FROM iam.project_shares WHERE project_id = $1 ORDER BY created_at, user_id;

-- name: DeleteShare :exec
DELETE FROM iam.project_shares WHERE project_id = $1 AND user_id = $2;

-- name: ListCloningProjects :many
-- Clones still being copied, for the cleanup loop (projects_cloning).
SELECT * FROM iam.projects
WHERE state = 'cloning' AND deleting_at IS NULL AND created_at < sqlc.arg(before)
ORDER BY created_at, id
LIMIT sqlc.arg(row_limit);

-- name: UpsertPendingShare :one
-- Sharing with the same address again refreshes the pending share (access,
-- sharer, expiry), keeping its creation time.
INSERT INTO iam.pending_shares (project_id, email, access, shared_by, created_at, updated_at, expires_at)
VALUES (sqlc.arg(project_id), sqlc.arg(email), sqlc.arg(access), sqlc.arg(shared_by), sqlc.arg(at), sqlc.arg(at), sqlc.arg(expires_at))
ON CONFLICT (project_id, email) DO UPDATE
SET access = excluded.access, shared_by = excluded.shared_by, updated_at = excluded.updated_at, expires_at = excluded.expires_at
RETURNING *;

-- name: ListPendingShares :many
SELECT * FROM iam.pending_shares WHERE project_id = $1 ORDER BY created_at, email;

-- name: ListPendingSharesTo :many
-- The unexpired pending shares to an address, in lock order (by project).
SELECT * FROM iam.pending_shares WHERE email = $1 AND expires_at > $2 ORDER BY project_id;

-- name: DeletePendingShare :execrows
DELETE FROM iam.pending_shares WHERE project_id = $1 AND email = $2;
