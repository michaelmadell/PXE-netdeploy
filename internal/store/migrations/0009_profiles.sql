-- netdeploy: foreign_keys=off
-- Profiles (ADR 0024) and clients.profile_id as a foreign key.

CREATE TABLE profiles (
    id                      TEXT    PRIMARY KEY,
    name                    TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    description             TEXT    NOT NULL DEFAULT '',
    boot_image_id           TEXT    NOT NULL REFERENCES boot_images (id) ON DELETE RESTRICT,
    install_image_id        TEXT    NOT NULL REFERENCES images (id) ON DELETE RESTRICT,
    edition_index           INTEGER NOT NULL CHECK (edition_index >= 1),
    install_method          TEXT    NOT NULL CHECK (install_method IN ('agent', 'smb')),
    -- Foreign keys once SMB targets (decision 4) and templates (Phase 5) exist.
    smb_target_id           TEXT,
    unattend_template_id    TEXT,
    -- Identity and OsSettings as the API has them.
    identity                TEXT    NOT NULL CHECK (json_valid(identity) AND json_extract(identity, '$.mode') IN ('none', 'local', 'ad', 'entra')),
    os_settings             TEXT    CHECK (os_settings IS NULL OR (json_valid(os_settings) AND json_type(os_settings) = 'object')),
    variables               TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(variables) AND json_type(variables) = 'object'),
    boot_order_after_deploy TEXT    NOT NULL CHECK (boot_order_after_deploy IN ('disk-first', 'network-first')),
    version                 INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at              INTEGER NOT NULL,
    updated_at              INTEGER NOT NULL,
    CHECK (install_method <> 'smb' OR smb_target_id IS NOT NULL)
) STRICT;

CREATE INDEX profiles_boot_image ON profiles (boot_image_id);
CREATE INDEX profiles_install_image ON profiles (install_image_id);

-- OS driver groups, in order. A group in use can't be deleted (RESTRICT).
CREATE TABLE profile_os_driver_groups (
    profile_id TEXT    NOT NULL REFERENCES profiles (id) ON DELETE CASCADE,
    group_id   TEXT    NOT NULL REFERENCES driver_groups (id) ON DELETE RESTRICT,
    position   INTEGER NOT NULL CHECK (position >= 0),
    PRIMARY KEY (profile_id, group_id)
) STRICT, WITHOUT ROWID;

CREATE INDEX profile_os_driver_groups_group ON profile_os_driver_groups (group_id);

-- clients: profile_id becomes a foreign key (SQLite's 12-step rebuild).
-- No profiles existed, so any stored value is dropped.
CREATE TABLE clients_new (
    id           TEXT    PRIMARY KEY,
    mac          TEXT    NOT NULL UNIQUE CHECK (mac GLOB '[0-9a-f][0-9a-f]:[0-9a-f][0-9a-f]:[0-9a-f][0-9a-f]:[0-9a-f][0-9a-f]:[0-9a-f][0-9a-f]:[0-9a-f][0-9a-f]'),
    smbios_uuid  TEXT,
    serial       TEXT,
    hostname     TEXT,
    profile_id   TEXT    REFERENCES profiles (id) ON DELETE RESTRICT,
    variables    TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(variables) AND json_type(variables) = 'object'),
    reserved_ip  TEXT    UNIQUE,
    source       TEXT    NOT NULL CHECK (source IN ('manual', 'discovered')),
    last_seen_at INTEGER,
    last_ip      TEXT,
    last_arch    TEXT    CHECK (last_arch IN ('bios', 'uefi-x64', 'uefi-x64-http', 'uefi-arm64', 'uefi-arm64-http')),
    version      INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
) STRICT;

INSERT INTO clients_new SELECT id, mac, smbios_uuid, serial, hostname, NULL, variables, reserved_ip, source,
    last_seen_at, last_ip, last_arch, version, created_at, updated_at FROM clients;
DROP TABLE clients;
ALTER TABLE clients_new RENAME TO clients;
CREATE INDEX clients_profile ON clients (profile_id) WHERE profile_id IS NOT NULL;
