-- +goose Up

-- Shares to an email address with no verified account yet. Signing in with
-- that address (verified) turns them into project_shares rows; unclaimed,
-- they expire and the cleanup loop removes them.
CREATE TABLE iam.pending_shares (
    project_id uuid        NOT NULL REFERENCES iam.projects (id),
    email      text COLLATE "C" NOT NULL, -- lowercased
    access     text        NOT NULL CHECK (access IN ('viewer', 'editor')),
    shared_by  text COLLATE "C" NOT NULL, -- '' once the sharer is deleted
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (project_id, email) -- sharing again refreshes the one pending share
);
CREATE INDEX pending_shares_email     ON iam.pending_shares (email);
CREATE INDEX pending_shares_expires   ON iam.pending_shares (expires_at);
CREATE INDEX pending_shares_shared_by ON iam.pending_shares (shared_by) WHERE shared_by <> '';

-- +goose Down
DROP TABLE iam.pending_shares;
