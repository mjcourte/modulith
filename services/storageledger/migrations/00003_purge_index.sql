-- The purge job lists deleted objects, longest-deleted first (ListDeleted).
-- A partial index holds only deleted rows, so it stays small however many
-- live objects there are.

-- +goose Up
CREATE INDEX objects_deleted ON ledger.objects (deleted_at, id) WHERE deleted_at IS NOT NULL;

-- +goose Down
DROP INDEX ledger.objects_deleted;
