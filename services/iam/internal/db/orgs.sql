-- name: InsertOrg :exec
INSERT INTO iam.orgs (id, name, created_at, updated_at) VALUES ($1, $2, $3, $3);

-- name: GetOrg :one
SELECT * FROM iam.orgs WHERE id = $1;

-- name: GetOrgForUpdate :one
-- Serialises membership changes: the admin rule is checked over the whole
-- member list, which must not change underneath.
SELECT * FROM iam.orgs WHERE id = $1 FOR UPDATE;

-- name: GetOrgForShare :one
-- Taken before adding a reference to the org (a project, invite or member),
-- so marking it deleting waits for it, and the other way round.
SELECT * FROM iam.orgs WHERE id = $1 FOR SHARE;

-- name: RenameOrg :exec
UPDATE iam.orgs SET name = $2, updated_at = $3 WHERE id = $1;

-- name: InsertMembership :exec
INSERT INTO iam.memberships (user_id, org_id, role, joined_at) VALUES ($1, $2, $3, $4);

-- name: GetMembership :one
SELECT * FROM iam.memberships WHERE user_id = $1;

-- name: ListMembers :many
-- Admins first, then by join time, as directory.sortMembers.
SELECT * FROM iam.memberships WHERE org_id = $1 ORDER BY role = 'admin' DESC, joined_at, user_id;

-- name: SetRole :exec
UPDATE iam.memberships SET role = $3 WHERE org_id = $1 AND user_id = $2;

-- name: DeleteMembership :exec
DELETE FROM iam.memberships WHERE org_id = $1 AND user_id = $2;
