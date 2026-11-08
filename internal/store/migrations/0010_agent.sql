-- The agent API (ADR 0025): tokens, each deployment's plan and the files it
-- may download, and deployment logs.

-- Bearer tokens, by SHA-256 of the token. session: single-use boot prompt
-- unlock (client_id); deployment: one running deployment.
CREATE TABLE agent_tokens (
    hash          TEXT    PRIMARY KEY CHECK (length(hash) = 64 AND hash NOT GLOB '*[^0-9a-f]*'),
    kind          TEXT    NOT NULL CHECK (kind IN ('session', 'deployment')),
    client_id     TEXT    NOT NULL REFERENCES clients (id) ON DELETE CASCADE,
    deployment_id TEXT    REFERENCES deployments (id) ON DELETE CASCADE,
    expires_at    INTEGER NOT NULL,
    used_at       INTEGER,
    created_at    INTEGER NOT NULL,
    CHECK ((kind = 'deployment') = (deployment_id IS NOT NULL))
) STRICT, WITHOUT ROWID;

CREATE INDEX agent_tokens_deployment ON agent_tokens (deployment_id) WHERE deployment_id IS NOT NULL;
CREATE INDEX agent_tokens_expires ON agent_tokens (expires_at);

-- The plan the agent fetches (AgentDeploymentPlan), built when the
-- deployment starts running.
ALTER TABLE deployments ADD COLUMN plan TEXT CHECK (plan IS NULL OR (json_valid(plan) AND json_type(plan) = 'object'));

-- Files the plan lists: what GET /agent/files/{sha256} may serve. No
-- foreign key to blobs: a running deployment keeps its files through
-- blob_refs (so deleting the image meanwhile doesn't break it); a finished
-- one's rows are history.
CREATE TABLE deployment_files (
    deployment_id TEXT NOT NULL REFERENCES deployments (id) ON DELETE CASCADE,
    sha256        TEXT NOT NULL CHECK (length(sha256) = 64 AND sha256 NOT GLOB '*[^0-9a-f]*'),
    PRIMARY KEY (deployment_id, sha256)
) STRICT, WITHOUT ROWID;

DROP VIEW blob_refs;
CREATE VIEW blob_refs (sha256) AS
    SELECT iso_blob FROM images WHERE iso_blob IS NOT NULL
    UNION ALL SELECT sha256 FROM image_boot_managers
    UNION ALL SELECT f.sha256 FROM deployment_files f JOIN deployments d ON d.id = f.deployment_id
        WHERE d.status = 'running';

CREATE TABLE deployment_logs (
    deployment_id TEXT    NOT NULL REFERENCES deployments (id) ON DELETE CASCADE,
    seq           INTEGER NOT NULL CHECK (seq >= 1),
    at            INTEGER NOT NULL,
    level         TEXT    NOT NULL CHECK (level IN ('debug', 'info', 'warn', 'error')),
    message       TEXT    NOT NULL,
    source        TEXT,
    PRIMARY KEY (deployment_id, seq)
) STRICT, WITHOUT ROWID;
