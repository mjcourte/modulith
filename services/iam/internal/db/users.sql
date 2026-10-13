-- name: GetUser :one
SELECT * FROM iam.users WHERE id = $1;

-- name: GetUserForUpdate :one
SELECT * FROM iam.users WHERE id = $1 FOR UPDATE;

-- name: GetUserForShare :one
-- Taken before adding a reference to the user (a membership, a share), so
-- marking them deleting (an UPDATE) waits for it, and the other way round.
SELECT * FROM iam.users WHERE id = $1 FOR SHARE;

-- name: GetUserByEmail :one
SELECT * FROM iam.users WHERE email = $1;

-- name: InsertUser :execrows
-- 0 rows: a concurrent first sign-in of the same user got there first.
INSERT INTO iam.users (id, email, email_verified, name, provider_avatar_url, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $6)
ON CONFLICT (id) DO NOTHING;

-- name: UpdateUserProfile :exec
UPDATE iam.users
SET email = $2, email_verified = $3, name = $4, provider_avatar_url = $5, updated_at = $6
WHERE id = $1;
