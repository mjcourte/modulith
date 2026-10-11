-- +goose Up
CREATE SCHEMA IF NOT EXISTS ledger;

CREATE TABLE ledger.objects (
    -- COLLATE "C" compares bytes, matching Go's string ordering, so keyset
    -- pagination on (created_at, id) agrees with the in-memory implementation.
    id           text COLLATE "C" PRIMARY KEY,
    owner_kind   text        NOT NULL CHECK (owner_kind IN ('project', 'user', 'org', 'gyoza')),
    owner_id     text,                 -- NULL only for gyoza-owned objects
    created_by   text,                 -- NULL for platform-created objects, or once the creator is deleted
    project_id   text,                 -- NULL when not associated with a project
    kind         text,
    bucket       text        NOT NULL,
    key          text        NOT NULL,
    size_bytes   bigint      NOT NULL,
    content_type text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    deleted_at   timestamptz,

    CHECK ((owner_kind = 'gyoza') = (owner_id IS NULL)),
    CHECK (owner_kind <> 'project' OR project_id = owner_id)
);

-- One index per List scope, in page order, plus one for ClearCreatedBy.
CREATE INDEX objects_owner_page   ON ledger.objects (owner_kind, owner_id, created_at, id);
CREATE INDEX objects_project_page ON ledger.objects (project_id, created_at, id) WHERE project_id IS NOT NULL;
CREATE INDEX objects_created_by   ON ledger.objects (created_by) WHERE created_by IS NOT NULL;

-- +goose Down
DROP TABLE ledger.objects;
