-- Driver packages and groups (ADR 0019).
CREATE TABLE driver_packages (
    id              TEXT    PRIMARY KEY,
    name            TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    status          TEXT    NOT NULL CHECK (status IN ('importing', 'ready', 'failed')),
    error           TEXT    CHECK (error IS NULL OR (json_valid(error) AND json_type(error, '$.code') = 'text')),
    source_filename TEXT,
    upload_id       TEXT    REFERENCES uploads (id) ON DELETE SET NULL,
    size_bytes      INTEGER NOT NULL CHECK (size_bytes > 0),
    sha256          TEXT    NOT NULL CHECK (length(sha256) = 64 AND sha256 NOT GLOB '*[^0-9a-f]*'),
    tree_id         TEXT    REFERENCES trees (id) ON DELETE RESTRICT,
    warnings        TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(warnings) AND json_type(warnings) = 'array'),
    version         INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL,
    CHECK ((status = 'ready') = (tree_id IS NOT NULL)),
    CHECK ((status = 'failed') = (error IS NOT NULL))
) STRICT;

CREATE UNIQUE INDEX driver_packages_one_per_archive ON driver_packages (sha256) WHERE status <> 'failed';

CREATE TABLE driver_infs (
    id               TEXT    PRIMARY KEY,
    package_id       TEXT    NOT NULL REFERENCES driver_packages (id) ON DELETE CASCADE,
    path             TEXT    NOT NULL,
    class            TEXT    NOT NULL,
    role             TEXT    NOT NULL CHECK (role IN ('network', 'storage', 'other')),
    provider         TEXT,
    version          TEXT,
    driver_date      TEXT    CHECK (driver_date IS NULL OR driver_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'),
    architectures    TEXT    NOT NULL CHECK (json_valid(architectures) AND json_type(architectures) = 'array'),
    target_os        TEXT    NOT NULL CHECK (json_valid(target_os) AND json_type(target_os) = 'array'),
    signature        TEXT    NOT NULL CHECK (signature IN ('whql', 'vendor', 'unsigned', 'unknown')),
    signer           TEXT,
    signature_detail TEXT,
    enabled          INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    UNIQUE (package_id, path)
) STRICT;

CREATE INDEX driver_infs_package ON driver_infs (package_id);
CREATE INDEX driver_infs_class ON driver_infs (class COLLATE NOCASE);

-- fold: lower-case, for case-insensitive lookups.
CREATE TABLE driver_hardware_ids (
    inf_id      TEXT NOT NULL REFERENCES driver_infs (id) ON DELETE CASCADE,
    fold        TEXT NOT NULL CHECK (fold = lower(hardware_id)),
    hardware_id TEXT NOT NULL,
    PRIMARY KEY (inf_id, fold)
) STRICT, WITHOUT ROWID;

CREATE INDEX driver_hardware_ids_fold ON driver_hardware_ids (fold);

CREATE TABLE driver_groups (
    id          TEXT    PRIMARY KEY,
    name        TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    description TEXT    NOT NULL DEFAULT '',
    version     INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
) STRICT;

-- A package in a group can't be deleted (RESTRICT).
CREATE TABLE driver_group_packages (
    group_id   TEXT    NOT NULL REFERENCES driver_groups (id) ON DELETE CASCADE,
    package_id TEXT    NOT NULL REFERENCES driver_packages (id) ON DELETE RESTRICT,
    position   INTEGER NOT NULL CHECK (position >= 0),
    PRIMARY KEY (group_id, package_id)
) STRICT, WITHOUT ROWID;

CREATE INDEX driver_group_packages_package ON driver_group_packages (package_id);

-- Driver packages own content too.
DROP VIEW tree_refs;
CREATE VIEW tree_refs (tree_id) AS
    SELECT tree_id FROM images WHERE tree_id IS NOT NULL
    UNION ALL SELECT tree_id FROM driver_packages WHERE tree_id IS NOT NULL;
