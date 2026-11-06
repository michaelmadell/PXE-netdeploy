-- Phase 2 schema (ADR 0014). Timestamps are Unix milliseconds, UTC.

-- The whole Settings document (api/openapi.yaml) as one JSON object.
CREATE TABLE settings (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    version    INTEGER NOT NULL CHECK (version >= 1),
    doc        TEXT    NOT NULL CHECK (json_valid(doc) AND json_type(doc) = 'object'),
    updated_at INTEGER NOT NULL
) STRICT;

CREATE TABLE clients (
    id           TEXT    PRIMARY KEY,
    -- Normalised: lower-case, colon-separated.
    mac          TEXT    NOT NULL UNIQUE CHECK (mac GLOB '[0-9a-f][0-9a-f]:[0-9a-f][0-9a-f]:[0-9a-f][0-9a-f]:[0-9a-f][0-9a-f]:[0-9a-f][0-9a-f]:[0-9a-f][0-9a-f]'),
    smbios_uuid  TEXT,
    serial       TEXT,
    hostname     TEXT,
    -- No foreign key until profiles exist (Phase 4 rebuilds this table).
    profile_id   TEXT,
    variables    TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(variables) AND json_type(variables) = 'object'),
    reserved_ip  TEXT    UNIQUE,
    source       TEXT    NOT NULL CHECK (source IN ('manual', 'discovered')),
    last_seen_at INTEGER,
    last_ip      TEXT,
    last_arch    TEXT    CHECK (last_arch IN ('bios', 'uefi-x64', 'uefi-x64-http', 'uefi-arm64', 'uefi-arm64-http')),
    -- ETag. Not bumped by sightings (last_*), only by admin edits.
    version      INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
) STRICT;

CREATE INDEX clients_profile ON clients (profile_id) WHERE profile_id IS NOT NULL;

CREATE TABLE deployments (
    id               TEXT    PRIMARY KEY,
    client_id        TEXT    NOT NULL REFERENCES clients (id) ON DELETE CASCADE,
    profile_id       TEXT    NOT NULL,
    profile_snapshot TEXT    NOT NULL CHECK (json_valid(profile_snapshot) AND json_type(profile_snapshot) = 'object'),
    origin           TEXT    NOT NULL CHECK (origin IN ('scheduled', 'boot-prompt')),
    status           TEXT    NOT NULL CHECK (status IN ('pending', 'running', 'succeeded', 'failed', 'cancelled')),
    stage            TEXT    CHECK (stage IN ('checked-in', 'partitioning', 'applying-image', 'injecting-drivers', 'configuring', 'finalizing', 'rebooting')),
    percent          INTEGER NOT NULL DEFAULT 0 CHECK (percent BETWEEN 0 AND 100),
    message          TEXT    NOT NULL DEFAULT '',
    started_at       INTEGER,
    finished_at      INTEGER,
    version          INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL,
    CHECK ((status IN ('succeeded', 'failed', 'cancelled')) = (finished_at IS NOT NULL)),
    CHECK (status <> 'running' OR started_at IS NOT NULL)
) STRICT;

-- ADR 0013: at most one active (armed or running) deployment per client.
CREATE UNIQUE INDEX deployments_one_active ON deployments (client_id) WHERE status IN ('pending', 'running');
CREATE INDEX deployments_client ON deployments (client_id, id);
CREATE INDEX deployments_status ON deployments (status, id);

-- Bound leases are written on ACK/renewal; offers stay in memory.
-- Declined addresses (conflicts) are kept out of the pool until expires_at.
CREATE TABLE dhcp_leases (
    interface  TEXT    NOT NULL,
    ip         TEXT    NOT NULL,
    mac        TEXT    NOT NULL,
    hostname   TEXT,
    arch       TEXT    CHECK (arch IN ('bios', 'uefi-x64', 'uefi-x64-http', 'uefi-arm64', 'uefi-arm64-http')),
    state      TEXT    NOT NULL CHECK (state IN ('bound', 'declined')),
    expires_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (interface, ip)
) STRICT, WITHOUT ROWID;

CREATE UNIQUE INDEX dhcp_leases_bound_mac ON dhcp_leases (interface, mac) WHERE state = 'bound';
CREATE INDEX dhcp_leases_mac ON dhcp_leases (mac);
CREATE INDEX dhcp_leases_expiry ON dhcp_leases (expires_at);

-- Stored responses for POSTs carrying Idempotency-Key. scope is
-- "<METHOD> <route template>", so a key is only replayed on the same operation.
CREATE TABLE idempotency_keys (
    scope        TEXT    NOT NULL,
    key          TEXT    NOT NULL,
    request_hash TEXT    NOT NULL,
    status       INTEGER NOT NULL CHECK (status BETWEEN 100 AND 599),
    content_type TEXT    NOT NULL,
    body         BLOB    NOT NULL,
    created_at   INTEGER NOT NULL,
    PRIMARY KEY (scope, key)
) STRICT, WITHOUT ROWID;

CREATE INDEX idempotency_keys_created ON idempotency_keys (created_at);
