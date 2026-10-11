-- The cleanup job lists trash and completed upload sessions by age. Partial
-- indexes hold only those rows, so they stay small however much is live.

-- +goose Up
CREATE INDEX assets_trashed    ON storagemanager.assets  (deleted_at, id)   WHERE deleted_at IS NOT NULL;
CREATE INDEX folders_trashed   ON storagemanager.folders (deleted_at, id)   WHERE deleted_at IS NOT NULL;
CREATE INDEX uploads_completed ON storagemanager.uploads (completed_at, id) WHERE completed_at IS NOT NULL;
-- Versions are deleted by asset when an asset is purged. The primary key
-- (asset_id, number) already serves that.

-- +goose Down
DROP INDEX storagemanager.uploads_completed;
DROP INDEX storagemanager.folders_trashed;
DROP INDEX storagemanager.assets_trashed;
