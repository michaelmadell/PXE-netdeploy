-- Deployments (ADR 0026): the rendered computer name, the last agent
-- request (inactivity timeout), copies of the secrets a deployment uses, and
-- the client's ODJ blob.

ALTER TABLE deployments ADD COLUMN computer_name TEXT;
ALTER TABLE deployments ADD COLUMN last_activity_at INTEGER;

-- A secret's sealed value copied as is when the deployment is created: the
-- ciphertext is bound to secret id, kind and value version (ADR 0021), so
-- it still opens after the secret is rotated or deleted. Deleted when the
-- deployment finishes.
CREATE TABLE deployment_secrets (
    deployment_id   TEXT    NOT NULL REFERENCES deployments (id) ON DELETE CASCADE,
    secret_id       TEXT    NOT NULL,
    kind            TEXT    NOT NULL,
    value_version   INTEGER NOT NULL CHECK (value_version >= 1),
    key_fingerprint TEXT    NOT NULL CHECK (length(key_fingerprint) = 64),
    value           BLOB    NOT NULL,
    PRIMARY KEY (deployment_id, secret_id)
) STRICT, WITHOUT ROWID;

ALTER TABLE clients ADD COLUMN odj_blob_secret_id TEXT REFERENCES secrets (id) ON DELETE RESTRICT;
CREATE INDEX clients_odj_blob ON clients (odj_blob_secret_id) WHERE odj_blob_secret_id IS NOT NULL;
