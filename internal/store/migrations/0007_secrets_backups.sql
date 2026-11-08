-- netdeploy: foreign_keys=off
-- Secrets (ADR 0021), backup state (ADR 0020), and the system.backup job kind.

-- jobs: add 'system.backup' to the kind CHECK (SQLite's 12-step rebuild).
CREATE TABLE jobs_new (
    id               TEXT    PRIMARY KEY,
    kind             TEXT    NOT NULL CHECK (kind IN ('image.import', 'driverPackage.import', 'bootImage.build', 'system.backup')),
    status           TEXT    NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    resource_id      TEXT,
    params           TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(params) AND json_type(params) = 'object'),
    percent          INTEGER NOT NULL DEFAULT 0 CHECK (percent BETWEEN 0 AND 100),
    message          TEXT    NOT NULL DEFAULT '',
    error            TEXT    CHECK (error IS NULL OR (json_valid(error) AND json_type(error, '$.code') = 'text')),
    cancel_requested INTEGER NOT NULL DEFAULT 0 CHECK (cancel_requested IN (0, 1)),
    created_at       INTEGER NOT NULL,
    started_at       INTEGER,
    finished_at      INTEGER,
    CHECK ((status IN ('succeeded', 'failed', 'cancelled')) = (finished_at IS NOT NULL)),
    CHECK (status = 'queued' OR status = 'cancelled' OR started_at IS NOT NULL),
    CHECK ((status = 'failed') = (error IS NOT NULL))
) STRICT;
INSERT INTO jobs_new SELECT id, kind, status, resource_id, params, percent, message, error, cancel_requested, created_at, started_at, finished_at FROM jobs;
DROP TABLE jobs;
ALTER TABLE jobs_new RENAME TO jobs;
CREATE UNIQUE INDEX jobs_one_active ON jobs (resource_id)
    WHERE status IN ('queued', 'running') AND resource_id IS NOT NULL;
CREATE INDEX jobs_status ON jobs (status, id);
CREATE INDEX jobs_resource ON jobs (resource_id, id) WHERE resource_id IS NOT NULL;
CREATE INDEX jobs_finished ON jobs (finished_at) WHERE finished_at IS NOT NULL;

-- Values are AES-256-GCM under the data key, bound to (id, kind,
-- value_version); key_fingerprint says which key sealed them.
CREATE TABLE secrets (
    id               TEXT    PRIMARY KEY,
    name             TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    description      TEXT    NOT NULL DEFAULT '',
    kind             TEXT    NOT NULL CHECK (kind IN ('password', 'product-key', 'domain-credential', 'share-credential', 'odj-blob', 'autopilot-json', 'provisioning-package')),
    hint             TEXT,
    value            BLOB    NOT NULL,
    value_version    INTEGER NOT NULL DEFAULT 1 CHECK (value_version >= 1),
    value_updated_at INTEGER NOT NULL,
    key_fingerprint  TEXT    NOT NULL CHECK (length(key_fingerprint) = 64),
    version          INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL
) STRICT;

CREATE INDEX secrets_kind ON secrets (kind, id);

-- Small server state that isn't settings: backup results, when the secrets
-- key was last exported.
CREATE TABLE state (
    key        TEXT    PRIMARY KEY,
    value      TEXT    NOT NULL CHECK (json_valid(value)),
    updated_at INTEGER NOT NULL
) STRICT;
