-- owner_id was NULL for gyoza-owned objects, which forced the owner query to
-- use IS NOT DISTINCT FROM. Postgres can't use an index for that, so listing
-- by owner scanned the whole table. Store '' instead so the query can use =.

-- +goose Up
UPDATE catalogue.objects SET owner_id = '' WHERE owner_id IS NULL;
ALTER TABLE catalogue.objects ALTER COLUMN owner_id SET NOT NULL;
ALTER TABLE catalogue.objects DROP CONSTRAINT objects_check;
ALTER TABLE catalogue.objects ADD CONSTRAINT objects_owner_id_check
    CHECK ((owner_kind = 'gyoza') = (owner_id = ''));

-- +goose Down
ALTER TABLE catalogue.objects DROP CONSTRAINT objects_owner_id_check;
ALTER TABLE catalogue.objects ALTER COLUMN owner_id DROP NOT NULL;
UPDATE catalogue.objects SET owner_id = NULL WHERE owner_id = '';
ALTER TABLE catalogue.objects ADD CONSTRAINT objects_check
    CHECK ((owner_kind = 'gyoza') = (owner_id IS NULL));
