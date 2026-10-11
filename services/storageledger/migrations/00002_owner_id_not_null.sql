-- owner_id was NULL for gyoza-owned objects, which forced the owner query to
-- use IS NOT DISTINCT FROM. Postgres can't use an index for that, so listing
-- by owner scanned the whole table. Store '' instead so the query can use =.

-- +goose Up
UPDATE ledger.objects SET owner_id = '' WHERE owner_id IS NULL;
ALTER TABLE ledger.objects ALTER COLUMN owner_id SET NOT NULL;
ALTER TABLE ledger.objects DROP CONSTRAINT objects_check;
ALTER TABLE ledger.objects ADD CONSTRAINT objects_owner_id_check
    CHECK ((owner_kind = 'gyoza') = (owner_id = ''));

-- +goose Down
ALTER TABLE ledger.objects DROP CONSTRAINT objects_owner_id_check;
ALTER TABLE ledger.objects ALTER COLUMN owner_id DROP NOT NULL;
UPDATE ledger.objects SET owner_id = NULL WHERE owner_id = '';
ALTER TABLE ledger.objects ADD CONSTRAINT objects_check
    CHECK ((owner_kind = 'gyoza') = (owner_id IS NULL));
