-- Asset uses: which asset versions each project uses (dam/uses.go).

-- name: UseExists :one
SELECT EXISTS (
    SELECT 1 FROM storage.asset_uses
    WHERE project_id = $1 AND asset_id = $2 AND version = $3
);

-- name: GetAssetForShare :one
-- Locks the asset against concurrent trashing (which takes FOR UPDATE) while
-- a use is recorded, without serialising projects recording the same asset.
SELECT * FROM storage.assets WHERE id = $1 FOR SHARE;

-- name: InsertUse :exec
INSERT INTO storage.asset_uses (project_id, asset_id, version)
VALUES ($1, $2, $3)
ON CONFLICT DO NOTHING;

-- name: DeleteUse :exec
DELETE FROM storage.asset_uses
WHERE project_id = $1 AND asset_id = $2 AND version = $3;

-- name: ListUses :many
SELECT asset_id, version FROM storage.asset_uses
WHERE project_id = $1
ORDER BY asset_id, version;

-- name: CountUses :many
-- Projects using each asset, any version; assets nobody uses are absent.
SELECT asset_id, count(DISTINCT project_id)::int AS projects
FROM storage.asset_uses
WHERE asset_id = ANY(sqlc.arg(asset_ids)::uuid[])
GROUP BY asset_id;

-- name: DeleteProjectUses :execrows
DELETE FROM storage.asset_uses WHERE project_id = $1;
