-- +goose Up
CREATE SCHEMA IF NOT EXISTS storagemanager;

-- Conventions: IDs are UUIDv7 made in Go; timestamps are set by Go (so they
-- match the in-memory store and advance within a transaction); optional
-- text is '' rather than NULL; only references (parent_id, folder_id) use NULL.

CREATE TABLE storagemanager.folders (
    id          uuid PRIMARY KEY,
    owner_kind  text        NOT NULL CHECK (owner_kind IN ('project', 'user', 'org', 'gyoza')),
    owner_id    text        NOT NULL, -- '' for gyoza, as in the ledger
    parent_id   uuid REFERENCES storagemanager.folders (id),
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
CREATE UNIQUE INDEX folders_live_name ON storagemanager.folders (owner_kind, owner_id, parent_id, name_key)
    NULLS NOT DISTINCT WHERE deleted_at IS NULL;
CREATE INDEX folders_parent   ON storagemanager.folders (parent_id) WHERE parent_id IS NOT NULL;
CREATE INDEX folders_owner    ON storagemanager.folders (owner_kind, owner_id);
CREATE INDEX folders_deletion ON storagemanager.folders (deletion_id) WHERE deletion_id IS NOT NULL;
CREATE INDEX folders_creator  ON storagemanager.folders (created_by) WHERE created_by <> '';

CREATE TABLE storagemanager.assets (
    id              uuid PRIMARY KEY,
    owner_kind      text        NOT NULL CHECK (owner_kind IN ('project', 'user', 'org', 'gyoza')),
    owner_id        text        NOT NULL,
    project_id      text        NOT NULL DEFAULT '',
    folder_id       uuid REFERENCES storagemanager.folders (id),
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
CREATE INDEX assets_owner_page   ON storagemanager.assets (owner_kind, owner_id, id);
CREATE INDEX assets_project_page ON storagemanager.assets (project_id, id) WHERE project_id <> '';
CREATE INDEX assets_folder       ON storagemanager.assets (folder_id) WHERE folder_id IS NOT NULL;
CREATE INDEX assets_tags         ON storagemanager.assets USING gin (tags);
CREATE INDEX assets_deletion     ON storagemanager.assets (deletion_id) WHERE deletion_id IS NOT NULL;
CREATE INDEX assets_creator      ON storagemanager.assets (created_by) WHERE created_by <> '';

CREATE TABLE storagemanager.asset_versions (
    asset_id     uuid        NOT NULL REFERENCES storagemanager.assets (id),
    number       int         NOT NULL,
    object_id    text        NOT NULL, -- the ledger object
    content_type text        NOT NULL,
    size_bytes   bigint      NOT NULL,
    created_by   text        NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL,
    PRIMARY KEY (asset_id, number)
);
CREATE INDEX asset_versions_creator ON storagemanager.asset_versions (created_by) WHERE created_by <> '';

CREATE TABLE storagemanager.uploads (
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
CREATE INDEX uploads_expiry  ON storagemanager.uploads (expires_at, id) WHERE completed_at IS NULL;
CREATE INDEX uploads_creator ON storagemanager.uploads (created_by) WHERE completed_at IS NULL;

-- +goose Down
DROP TABLE storagemanager.uploads;
DROP TABLE storagemanager.asset_versions;
DROP TABLE storagemanager.assets;
DROP TABLE storagemanager.folders;
