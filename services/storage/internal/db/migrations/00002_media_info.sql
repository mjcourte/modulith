-- +goose Up

-- Media metadata (dimensions, duration, codecs) of ledger objects, extracted
-- once when stored so it can be read without fetching the file. At most one
-- row per object; objects that aren't media have none.
CREATE TABLE storage.media_info (
    object_id   text COLLATE "C" PRIMARY KEY
                REFERENCES storage.objects (id) ON DELETE CASCADE, -- purging an object drops its row
    status      text        NOT NULL CHECK (status IN ('pending', 'ready', 'failed')),
    -- Typed columns for what we filter or sort by; 0 means unknown.
    width       int         NOT NULL DEFAULT 0, -- display size, after rotation
    height      int         NOT NULL DEFAULT 0,
    duration_us bigint      NOT NULL DEFAULT 0, -- microseconds: exact for ffprobe's 6 decimals
    details     jsonb       NOT NULL DEFAULT '{}', -- everything else (streams, codecs), as mediaprocessor.Info
    error       text        NOT NULL DEFAULT '', -- why the last attempt failed
    attempts    int         NOT NULL DEFAULT 0,
    updated_at  timestamptz NOT NULL -- clock_timestamp(), set by the queries: it advances within a transaction
);
-- The probe job's work list, oldest first. Partial, so it stays small.
CREATE INDEX media_info_pending ON storage.media_info (updated_at, object_id) WHERE status = 'pending';

-- +goose Down
DROP TABLE storage.media_info;
