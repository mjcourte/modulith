-- +goose Up

-- Document snapshots (internal/snapshots), written by the documents service
-- through BeginSnapshot/CompleteSnapshot. A row is pending until completed;
-- completed rows have a version, counting a project's snapshots from 1. The
-- object itself is in storage.objects (Kind=snapshot) once completed.
CREATE TABLE storage.snapshots (
    object_id    text COLLATE "C" PRIMARY KEY, -- storage.objects.id once completed
    project_id   text        NOT NULL,         -- iam's project; no FK across schemas
    seq          bigint      NOT NULL CHECK (seq >= 0), -- last delta stream sequence covered
    version      int,                          -- NULL while pending
    edited_by    text,                         -- who made the last change it holds
    edited_at    timestamptz,                  -- when
    bucket       text        NOT NULL,
    key          text        NOT NULL,
    content_type text        NOT NULL,
    size_bytes   bigint      NOT NULL CHECK (size_bytes > 0),
    expires_at   timestamptz NOT NULL,         -- a pending row past this is abandoned
    created_at   timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    CHECK ((version IS NULL) = (completed_at IS NULL)),
    UNIQUE (project_id, version)               -- also serves latest and listing by project
);
-- The cleanup job's list of abandoned uploads.
CREATE INDEX snapshots_pending ON storage.snapshots (expires_at) WHERE completed_at IS NULL;

-- +goose Down
DROP TABLE storage.snapshots;
