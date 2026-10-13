-- name: UpsertInvite :one
-- Inviting the same address again refreshes the invite, keeping its ID and
-- creation time.
INSERT INTO iam.invites (id, org_id, email, role, invited_by, created_at, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (org_id, email) DO UPDATE
SET role = excluded.role, invited_by = excluded.invited_by, expires_at = excluded.expires_at
RETURNING *;

-- name: GetInvite :one
SELECT * FROM iam.invites WHERE id = $1;

-- name: GetInviteForUpdate :one
SELECT * FROM iam.invites WHERE id = $1 FOR UPDATE;

-- name: ListOrgInvites :many
SELECT * FROM iam.invites WHERE org_id = $1 ORDER BY id DESC;

-- name: ListEmailInvites :many
SELECT * FROM iam.invites WHERE email = $1 AND expires_at > $2 ORDER BY id DESC;

-- name: DeleteInvite :exec
DELETE FROM iam.invites WHERE id = $1;
