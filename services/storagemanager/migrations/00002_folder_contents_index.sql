-- Listing a folder's contents (the file browser's main query) scanned the
-- whole assets table: the generic query's `folder_id IS NOT DISTINCT FROM $x`
-- can't use an index (33ms for a 50k-asset owner in a 200k table, growing
-- with the table). Folder listings now use dedicated queries with
-- `folder_id = $x` / `folder_id IS NULL`, which this index serves in page order.

-- +goose Up
CREATE INDEX assets_folder_page ON storagemanager.assets (owner_kind, owner_id, folder_id, id);

-- +goose Down
DROP INDEX storagemanager.assets_folder_page;
