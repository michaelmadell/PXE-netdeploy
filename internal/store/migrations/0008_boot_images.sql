-- Boot images (ADR 0022). A row holds the inputs (BootImageInput) and the
-- build it serves: the last good build stays attached while the inputs
-- change, a rebuild runs or a rebuild fails.
CREATE TABLE boot_images (
    id                    TEXT    PRIMARY KEY,
    name                  TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    source_image_id       TEXT    NOT NULL REFERENCES images (id) ON DELETE RESTRICT,
    source_index          INTEGER NOT NULL CHECK (source_index >= 1),
    architecture          TEXT    NOT NULL CHECK (architecture IN ('x64', 'arm64')),
    agent_version         TEXT,
    server_urls           TEXT    NOT NULL CHECK (json_valid(server_urls) AND json_type(server_urls) = 'array' AND json_array_length(server_urls) >= 1),
    builder               TEXT    NOT NULL CHECK (builder IN ('auto', 'dism', 'wimlib')),
    optional_components   TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(optional_components) AND json_type(optional_components) = 'array'),
    bootmgr               TEXT    NOT NULL CHECK (bootmgr IN ('auto', 'windows-pca-2011', 'windows-uefi-ca-2023')),
    tftp_block_size       INTEGER NOT NULL CHECK (tftp_block_size BETWEEN 512 AND 16384),
    tftp_window_size      INTEGER NOT NULL CHECK (tftp_window_size BETWEEN 1 AND 64),
    produce_http_boot_iso INTEGER NOT NULL DEFAULT 0 CHECK (produce_http_boot_iso IN (0, 1)),
    -- draft: never built; building: a build job is queued or running;
    -- built: the last build succeeded; failed: the last build failed. The
    -- API's ready/stale is built compared with the current inputs.
    state                 TEXT    NOT NULL CHECK (state IN ('draft', 'building', 'built', 'failed')),
    -- The build being served.
    tree_id               TEXT    REFERENCES trees (id) ON DELETE RESTRICT,
    input_hash            TEXT    CHECK (input_hash IS NULL OR (length(input_hash) = 64 AND input_hash NOT GLOB '*[^0-9a-f]*')),
    built_inputs          TEXT    CHECK (built_inputs IS NULL OR (json_valid(built_inputs) AND json_type(built_inputs) = 'object')),
    built_with            TEXT    CHECK (built_with IS NULL OR built_with IN ('dism', 'wimlib')),
    built_at              INTEGER,
    selected_boot_manager TEXT    CHECK (selected_boot_manager IS NULL OR (json_valid(selected_boot_manager) AND json_type(selected_boot_manager) = 'object')),
    warnings              TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(warnings) AND json_type(warnings) = 'array'),
    error                 TEXT    CHECK (error IS NULL OR (json_valid(error) AND json_type(error, '$.code') = 'text')),
    -- No foreign key: finished jobs are purged after 30 days.
    last_job_id           TEXT,
    version               INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at            INTEGER NOT NULL,
    updated_at            INTEGER NOT NULL,
    CHECK ((tree_id IS NULL) = (input_hash IS NULL)),
    CHECK ((tree_id IS NULL) = (built_inputs IS NULL)),
    CHECK ((tree_id IS NULL) = (built_with IS NULL)),
    CHECK ((tree_id IS NULL) = (built_at IS NULL)),
    CHECK ((tree_id IS NULL) = (selected_boot_manager IS NULL)),
    CHECK (state <> 'built' OR tree_id IS NOT NULL),
    CHECK (state <> 'draft' OR tree_id IS NULL),
    CHECK ((state = 'failed') = (error IS NOT NULL))
) STRICT;

CREATE INDEX boot_images_source ON boot_images (source_image_id);
CREATE INDEX boot_images_hash ON boot_images (input_hash) WHERE input_hash IS NOT NULL;

-- A driver group used by a boot image can't be deleted (RESTRICT).
CREATE TABLE boot_image_driver_groups (
    boot_image_id TEXT    NOT NULL REFERENCES boot_images (id) ON DELETE CASCADE,
    group_id      TEXT    NOT NULL REFERENCES driver_groups (id) ON DELETE RESTRICT,
    position      INTEGER NOT NULL CHECK (position >= 0),
    PRIMARY KEY (boot_image_id, group_id)
) STRICT, WITHOUT ROWID;

CREATE INDEX boot_image_driver_groups_group ON boot_image_driver_groups (group_id);

-- Boot images own content too (ADR 0015's views). They own no blobs
-- outside their trees, so blob_refs is unchanged.
DROP VIEW tree_refs;
CREATE VIEW tree_refs (tree_id) AS
    SELECT tree_id FROM images WHERE tree_id IS NOT NULL
    UNION ALL SELECT tree_id FROM driver_packages WHERE tree_id IS NOT NULL
    UNION ALL SELECT tree_id FROM boot_images WHERE tree_id IS NOT NULL;
