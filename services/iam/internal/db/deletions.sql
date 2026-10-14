-- name: MarkUserDeleting :one
-- The first mark decides the time and the destination; later ones change nothing.
UPDATE iam.users
SET deleting_at = COALESCE(deleting_at, sqlc.arg(at)),
    deleting_to_org = CASE WHEN deleting_at IS NULL THEN sqlc.narg(to_org)::uuid ELSE deleting_to_org END
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: MarkOrgDeleting :one
UPDATE iam.orgs SET deleting_at = COALESCE(deleting_at, sqlc.arg(at)) WHERE id = sqlc.arg(id) RETURNING *;

-- name: MarkOrgProjectsDeleting :exec
UPDATE iam.projects SET deleting_at = sqlc.arg(at)
WHERE owner_kind = 'org' AND owner_id = sqlc.arg(org_id)::text AND deleting_at IS NULL;

-- name: MarkProjectDeleting :one
UPDATE iam.projects SET deleting_at = COALESCE(deleting_at, sqlc.arg(at)) WHERE id = sqlc.arg(id) RETURNING *;

-- name: ListDeletingUsers :many
SELECT * FROM iam.users WHERE deleting_at IS NOT NULL ORDER BY deleting_at, id LIMIT $1;

-- name: ListDeletingOrgs :many
SELECT * FROM iam.orgs WHERE deleting_at IS NOT NULL ORDER BY deleting_at, id LIMIT $1;

-- name: ListDeletingProjects :many
SELECT * FROM iam.projects WHERE deleting_at IS NOT NULL ORDER BY deleting_at, id LIMIT $1;

-- name: ListOwnedProjects :many
SELECT id FROM iam.projects WHERE owner_kind = $1 AND owner_id = $2 ORDER BY id LIMIT $3;

-- name: CountOwnedProjects :one
SELECT count(*) FROM iam.projects WHERE owner_kind = $1 AND owner_id = $2;

-- name: TransferUserProjects :execrows
-- User projects are never locked, so none arrives locked.
UPDATE iam.projects SET owner_kind = 'org', owner_id = sqlc.arg(org_id)::text, updated_at = sqlc.arg(at)
WHERE owner_kind = 'user' AND owner_id = sqlc.arg(user_id)::text;

-- name: DeleteSharesTo :execrows
DELETE FROM iam.project_shares WHERE user_id = $1;

-- name: ClearSharedBy :execrows
UPDATE iam.project_shares SET shared_by = '' WHERE shared_by = $1;

-- name: DeleteInvitesTo :execrows
DELETE FROM iam.invites WHERE email = $1;

-- name: ClearInvitedBy :execrows
UPDATE iam.invites SET invited_by = '' WHERE invited_by = $1;

-- name: ClearProjectCreator :execrows
UPDATE iam.projects SET created_by = '' WHERE created_by = $1;

-- name: DeleteProjectShares :exec
DELETE FROM iam.project_shares WHERE project_id = $1;

-- name: DeleteProject :exec
DELETE FROM iam.projects WHERE id = $1;

-- name: DeleteOrgMemberships :exec
DELETE FROM iam.memberships WHERE org_id = $1;

-- name: DeleteOrgInvites :exec
DELETE FROM iam.invites WHERE org_id = $1;

-- name: DeleteOrg :exec
DELETE FROM iam.orgs WHERE id = $1;

-- name: DeleteUser :exec
DELETE FROM iam.users WHERE id = $1;

-- name: DeleteExpiredInvites :execrows
-- Up to a batch at a time, longest-expired first.
DELETE FROM iam.invites
WHERE id IN (SELECT i.id FROM iam.invites i WHERE i.expires_at <= $1 ORDER BY i.expires_at LIMIT $2);

-- name: DeletePendingSharesTo :execrows
DELETE FROM iam.pending_shares WHERE email = $1;

-- name: ClearPendingSharedBy :execrows
UPDATE iam.pending_shares SET shared_by = '' WHERE shared_by = $1;

-- name: DeleteProjectPendingShares :exec
DELETE FROM iam.pending_shares WHERE project_id = $1;

-- name: DeleteExpiredPendingShares :execrows
-- Up to a batch at a time, longest-expired first.
DELETE FROM iam.pending_shares
WHERE (project_id, email) IN (
    SELECT ps.project_id, ps.email FROM iam.pending_shares ps WHERE ps.expires_at <= $1 ORDER BY ps.expires_at LIMIT $2
);
