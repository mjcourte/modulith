-- +goose Up

-- The cleanup loop lists clones still being copied, oldest first. Partial:
-- clones are ready within seconds, so this stays nearly empty.
CREATE INDEX projects_cloning ON iam.projects (created_at, id) WHERE state = 'cloning' AND deleting_at IS NULL;

-- +goose Down
DROP INDEX iam.projects_cloning;
