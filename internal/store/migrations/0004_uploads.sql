-- tus uploads (ADR 0017). The bytes are in <content>/uploads/<id>.
CREATE TABLE uploads (
    id         TEXT    PRIMARY KEY,
    length     INTEGER NOT NULL CHECK (length > 0),
    -- Bytes received (the tus offset).
    received   INTEGER NOT NULL DEFAULT 0 CHECK (received BETWEEN 0 AND length),
    filename   TEXT,
    filetype   TEXT,
    -- SHA-256 state after `received` bytes (encoding.BinaryMarshaler).
    hash_state BLOB    NOT NULL,
    sha256     TEXT    CHECK (sha256 IS NULL OR (length(sha256) = 64 AND sha256 NOT GLOB '*[^0-9a-f]*')),
    -- An import from it succeeded: kept until deleted, never expires.
    imported   INTEGER NOT NULL DEFAULT 0 CHECK (imported IN (0, 1)),
    -- The most recent import job to claim it; in use while that job is
    -- queued or running.
    job_id     TEXT    REFERENCES jobs (id) ON DELETE SET NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    expires_at INTEGER,
    CHECK ((sha256 IS NOT NULL) = (received = length)),
    CHECK ((imported = 1) = (expires_at IS NULL)),
    CHECK (imported = 0 OR sha256 IS NOT NULL)
) STRICT;

CREATE INDEX uploads_expires ON uploads (expires_at) WHERE expires_at IS NOT NULL;
CREATE INDEX uploads_job ON uploads (job_id) WHERE job_id IS NOT NULL;
