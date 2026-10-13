-- +goose Up
CREATE SCHEMA IF NOT EXISTS iam;

-- Conventions (as in storage): UUIDv7 IDs and timestamps are made in Go, so
-- they match the memory store; optional text is '' rather than NULL; only
-- references use NULL. Text compared or sorted by Go uses COLLATE "C"
-- (bytes), so both stores order alike.

CREATE TABLE iam.users (
    id                  text COLLATE "C" PRIMARY KEY, -- the SuperTokens user ID
    email               text COLLATE "C" NOT NULL UNIQUE, -- lowercased by Go
    email_verified      boolean     NOT NULL,
    name                text        NOT NULL,
    provider_avatar_url text        NOT NULL,
    created_at          timestamptz NOT NULL,
    updated_at          timestamptz NOT NULL,
    deleting_at         timestamptz,
    -- Where a deleting user's things go. No foreign key: the org can be
    -- deleted before the user's deletion finishes.
    deleting_to_org     uuid
);
CREATE INDEX users_deleting ON iam.users (deleting_at, id) WHERE deleting_at IS NOT NULL;

CREATE TABLE iam.orgs (
    id          uuid PRIMARY KEY,
    name        text        NOT NULL,
    created_at  timestamptz NOT NULL,
    updated_at  timestamptz NOT NULL,
    deleting_at timestamptz
);
CREATE INDEX orgs_deleting ON iam.orgs (deleting_at, id) WHERE deleting_at IS NOT NULL;

-- One row per user at most: the primary key is what keeps a user in one org.
CREATE TABLE iam.memberships (
    user_id   text COLLATE "C" PRIMARY KEY REFERENCES iam.users (id),
    org_id    uuid        NOT NULL REFERENCES iam.orgs (id),
    role      text        NOT NULL CHECK (role IN ('admin', 'member')),
    joined_at timestamptz NOT NULL
);
CREATE INDEX memberships_org ON iam.memberships (org_id);

CREATE TABLE iam.invites (
    id         uuid PRIMARY KEY,
    org_id     uuid        NOT NULL REFERENCES iam.orgs (id),
    email      text COLLATE "C" NOT NULL,
    role       text        NOT NULL CHECK (role IN ('admin', 'member')),
    invited_by text COLLATE "C" NOT NULL, -- '' once the inviter is deleted
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    UNIQUE (org_id, email) -- inviting again refreshes the one invite
);
CREATE INDEX invites_email      ON iam.invites (email);
CREATE INDEX invites_expires    ON iam.invites (expires_at);
CREATE INDEX invites_invited_by ON iam.invites (invited_by) WHERE invited_by <> '';

CREATE TABLE iam.projects (
    id          uuid PRIMARY KEY,
    -- No foreign key to the owner: it's a user, an org or gyoza ('' ID). The
    -- Store checks it exists, under a lock on the owner's row.
    owner_kind  text COLLATE "C" NOT NULL CHECK (owner_kind IN ('user', 'org', 'gyoza')),
    owner_id    text COLLATE "C" NOT NULL,
    name        text        NOT NULL,
    state       text        NOT NULL CHECK (state IN ('ready', 'cloning')),
    cloned_from uuid,       -- no foreign key: the source may be deleted since
    org_locked  boolean     NOT NULL,
    created_by  text COLLATE "C" NOT NULL, -- '' for tooling, or once the creator is deleted
    created_at  timestamptz NOT NULL,
    updated_at  timestamptz NOT NULL,
    deleting_at timestamptz,
    CHECK ((owner_kind = 'gyoza') = (owner_id = '')),
    CHECK (NOT org_locked OR owner_kind = 'org')
);
-- Listing by owner, in page order (also "my projects": mine and my org's).
CREATE INDEX projects_owner_page ON iam.projects (owner_kind, owner_id, id);
CREATE INDEX projects_created_by ON iam.projects (created_by) WHERE created_by <> '';
CREATE INDEX projects_deleting   ON iam.projects (deleting_at, id) WHERE deleting_at IS NOT NULL;

CREATE TABLE iam.project_shares (
    project_id uuid        NOT NULL REFERENCES iam.projects (id),
    user_id    text COLLATE "C" NOT NULL REFERENCES iam.users (id),
    access     text        NOT NULL CHECK (access IN ('viewer', 'editor')),
    shared_by  text COLLATE "C" NOT NULL, -- '' once the sharer is deleted
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (project_id, user_id)
);
-- "Shared with me", in page order.
CREATE INDEX project_shares_user      ON iam.project_shares (user_id, project_id);
CREATE INDEX project_shares_shared_by ON iam.project_shares (shared_by) WHERE shared_by <> '';

-- +goose Down
DROP TABLE iam.project_shares;
DROP TABLE iam.projects;
DROP TABLE iam.invites;
DROP TABLE iam.memberships;
DROP TABLE iam.orgs;
DROP TABLE iam.users;
