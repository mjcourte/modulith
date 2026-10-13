-- +goose Up

-- Which asset versions each project uses, kept by documentmanager as refs are
-- added to and removed from documents. Reading through a project and copying
-- a project's files both start here.
CREATE TABLE storage.asset_uses (
    project_id text        NOT NULL, -- iam's project; no FK across schemas
    asset_id   uuid        NOT NULL,
    version    int         NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, asset_id, version), -- also serves listing by project
    -- Purging an asset deletes its versions, and with them its uses.
    FOREIGN KEY (asset_id, version) REFERENCES storage.asset_versions (asset_id, number) ON DELETE CASCADE
);
-- Counting an asset's uses (trash warnings), and the cascade above.
CREATE INDEX asset_uses_asset ON storage.asset_uses (asset_id, version);

-- +goose Down
DROP TABLE storage.asset_uses;
