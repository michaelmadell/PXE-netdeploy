-- Jobs (ADR 0016). The table is the queue: queued rows run oldest first (ids
-- are ULIDs) per lane; at most one queued or running job per resource.
CREATE TABLE jobs (
    id               TEXT    PRIMARY KEY,
    kind             TEXT    NOT NULL CHECK (kind IN ('image.import', 'driverPackage.import', 'bootImage.build')),
    status           TEXT    NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    resource_id      TEXT,
    params           TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(params) AND json_type(params) = 'object'),
    percent          INTEGER NOT NULL DEFAULT 0 CHECK (percent BETWEEN 0 AND 100),
    message          TEXT    NOT NULL DEFAULT '',
    -- Problem JSON (RFC 9457) with a code; present exactly when failed.
    error            TEXT    CHECK (error IS NULL OR (json_valid(error) AND json_type(error, '$.code') = 'text')),
    cancel_requested INTEGER NOT NULL DEFAULT 0 CHECK (cancel_requested IN (0, 1)),
    created_at       INTEGER NOT NULL,
    started_at       INTEGER,
    finished_at      INTEGER,
    CHECK ((status IN ('succeeded', 'failed', 'cancelled')) = (finished_at IS NOT NULL)),
    CHECK (status = 'queued' OR status = 'cancelled' OR started_at IS NOT NULL),
    CHECK ((status = 'failed') = (error IS NOT NULL))
) STRICT;

CREATE UNIQUE INDEX jobs_one_active ON jobs (resource_id)
    WHERE status IN ('queued', 'running') AND resource_id IS NOT NULL;
CREATE INDEX jobs_status ON jobs (status, id);
CREATE INDEX jobs_resource ON jobs (resource_id, id) WHERE resource_id IS NOT NULL;
CREATE INDEX jobs_finished ON jobs (finished_at) WHERE finished_at IS NOT NULL;
