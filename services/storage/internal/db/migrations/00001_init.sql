-- +goose Up
CREATE SCHEMA IF NOT EXISTS storage;

-- The ledger: one row per object stored in S3, whatever uses it.
CREATE TABLE storage.objects (
    -- COLLATE "C" compares bytes, matching Go's string ordering, so keyset
    -- pagination on (created_at, id) agrees with the in-memory implementation.
    id           text COLLATE "C" PRIMARY KEY,
    owner_kind   text        NOT NULL CHECK (owner_kind IN ('project', 'user', 'org', 'modulith')),
    -- '' (not NULL) for modulith-owned objects, so the owner query can use = and
    -- its index; IS NOT DISTINCT FROM can't use one.
    owner_id     text        NOT NULL,
    created_by   text,                 -- NULL for platform-created objects, or once the creator is deleted
    project_id   text,                 -- NULL when not associated with a project
    kind         text,
    bucket       text        NOT NULL,
    key          text        NOT NULL,
    size_bytes   bigint      NOT NULL,
    content_type text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    deleted_at   timestamptz,

    CONSTRAINT objects_owner_id_check CHECK ((owner_kind = 'modulith') = (owner_id = '')),
    CHECK (owner_kind <> 'project' OR project_id = owner_id)
);

-- One index per List scope, in page order, plus one for ClearCreatedBy.
CREATE INDEX objects_owner_page   ON storage.objects (owner_kind, owner_id, created_at, id);
CREATE INDEX objects_project_page ON storage.objects (project_id, created_at, id) WHERE project_id IS NOT NULL;
CREATE INDEX objects_created_by   ON storage.objects (created_by) WHERE created_by IS NOT NULL;
-- The purge job lists deleted objects, longest-deleted first (ListDeleted).
-- Partial, so it stays small however many live objects there are.
CREATE INDEX objects_deleted      ON storage.objects (deleted_at, id) WHERE deleted_at IS NOT NULL;

-- The DAM. Conventions: IDs are UUIDv7 made in Go; timestamps are set by Go
-- (so they match the in-memory store and advance within a transaction);
-- optional text is '' rather than NULL; only references (parent_id,
-- folder_id) use NULL.

CREATE TABLE storage.folders (
    id          uuid PRIMARY KEY,
    owner_kind  text        NOT NULL CHECK (owner_kind IN ('project', 'user', 'org', 'modulith')),
    owner_id    text        NOT NULL, -- '' for modulith, as in objects
    parent_id   uuid REFERENCES storage.folders (id),
    name        text        NOT NULL,
    name_key    text        NOT NULL, -- folderNameKey(name), computed in Go
    created_by  text        NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL,
    updated_at  timestamptz NOT NULL,
    deleted_at  timestamptz,
    deletion_id uuid
);
-- Live siblings can't share a name, ignoring case. NULLS NOT DISTINCT makes
-- two top-level folders (parent_id NULL) count as siblings.
CREATE UNIQUE INDEX folders_live_name ON storage.folders (owner_kind, owner_id, parent_id, name_key)
    NULLS NOT DISTINCT WHERE deleted_at IS NULL;
CREATE INDEX folders_parent   ON storage.folders (parent_id) WHERE parent_id IS NOT NULL;
CREATE INDEX folders_owner    ON storage.folders (owner_kind, owner_id);
CREATE INDEX folders_deletion ON storage.folders (deletion_id) WHERE deletion_id IS NOT NULL;
CREATE INDEX folders_creator  ON storage.folders (created_by) WHERE created_by <> '';
-- The cleanup job lists trash by age; partial, so it stays small.
CREATE INDEX folders_trashed  ON storage.folders (deleted_at, id) WHERE deleted_at IS NOT NULL;

CREATE TABLE storage.assets (
    id              uuid PRIMARY KEY,
    owner_kind      text        NOT NULL CHECK (owner_kind IN ('project', 'user', 'org', 'modulith')),
    owner_id        text        NOT NULL,
    project_id      text        NOT NULL DEFAULT '',
    folder_id       uuid REFERENCES storage.folders (id),
    name            text        NOT NULL,
    description     text        NOT NULL DEFAULT '',
    tags            text[]      NOT NULL DEFAULT '{}',
    content_type    text        NOT NULL,
    category        text        NOT NULL,
    current_version int         NOT NULL,
    created_by      text        NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL,
    updated_at      timestamptz NOT NULL,
    deleted_at      timestamptz,
    deletion_id     uuid,
    CHECK (owner_kind <> 'project' OR project_id = owner_id)
);
-- One index per listing scope, in page order (by id: UUIDv7 = creation order).
CREATE INDEX assets_owner_page   ON storage.assets (owner_kind, owner_id, id);
CREATE INDEX assets_project_page ON storage.assets (project_id, id) WHERE project_id <> '';
-- Folder listings (the file browser's main query) use `folder_id = $x` /
-- `folder_id IS NULL` rather than IS NOT DISTINCT FROM, so this index serves
-- them in page order.
CREATE INDEX assets_folder_page  ON storage.assets (owner_kind, owner_id, folder_id, id);
CREATE INDEX assets_folder       ON storage.assets (folder_id) WHERE folder_id IS NOT NULL;
CREATE INDEX assets_tags         ON storage.assets USING gin (tags);
CREATE INDEX assets_deletion     ON storage.assets (deletion_id) WHERE deletion_id IS NOT NULL;
CREATE INDEX assets_creator      ON storage.assets (created_by) WHERE created_by <> '';
CREATE INDEX assets_trashed      ON storage.assets (deleted_at, id) WHERE deleted_at IS NOT NULL;

CREATE TABLE storage.asset_versions (
    asset_id     uuid        NOT NULL REFERENCES storage.assets (id),
    number       int         NOT NULL,
    object_id    text        NOT NULL, -- the objects row
    content_type text        NOT NULL,
    size_bytes   bigint      NOT NULL,
    created_by   text        NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL,
    PRIMARY KEY (asset_id, number) -- also serves deleting versions by asset
);
CREATE INDEX asset_versions_creator ON storage.asset_versions (created_by) WHERE created_by <> '';

CREATE TABLE storage.uploads (
    id              uuid PRIMARY KEY,
    owner_kind      text        NOT NULL,
    owner_id        text        NOT NULL,
    project_id      text        NOT NULL DEFAULT '',
    created_by      text        NOT NULL,
    asset_id        uuid, -- no FK: the purge job may remove assets before old sessions
    name            text        NOT NULL DEFAULT '',
    description     text        NOT NULL DEFAULT '',
    tags            text[]      NOT NULL DEFAULT '{}',
    folder_id       uuid,
    bucket          text        NOT NULL,
    key             text        NOT NULL,
    content_type    text        NOT NULL,
    size_bytes      bigint      NOT NULL,
    object_id       text        NOT NULL DEFAULT '',
    expires_at      timestamptz NOT NULL,
    created_at      timestamptz NOT NULL,
    completed_at    timestamptz,
    result_asset_id uuid,
    result_version  int         NOT NULL DEFAULT 0
);
CREATE INDEX uploads_expiry    ON storage.uploads (expires_at, id) WHERE completed_at IS NULL;
CREATE INDEX uploads_creator   ON storage.uploads (created_by) WHERE completed_at IS NULL;
CREATE INDEX uploads_completed ON storage.uploads (completed_at, id) WHERE completed_at IS NOT NULL;

-- +goose Down
DROP TABLE storage.uploads;
DROP TABLE storage.asset_versions;
DROP TABLE storage.assets;
DROP TABLE storage.folders;
DROP TABLE storage.objects;
