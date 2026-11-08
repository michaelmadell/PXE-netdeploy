-- Imported images (ADR 0018). The extracted files are a content store tree;
-- boot managers found on the media and inside its WIMs are blobs.
CREATE TABLE images (
    id              TEXT    PRIMARY KEY,
    name            TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    notes           TEXT    NOT NULL DEFAULT '',
    kind            TEXT    CHECK (kind IN ('windows', 'linux', 'other')),
    status          TEXT    NOT NULL CHECK (status IN ('importing', 'ready', 'failed')),
    error           TEXT    CHECK (error IS NULL OR (json_valid(error) AND json_type(error, '$.code') = 'text')),
    -- The ISO: what it was called, its label, size and hash. Kept after its
    -- upload is deleted, so the media can still be identified.
    source_filename TEXT,
    volume_label    TEXT,
    upload_id       TEXT    REFERENCES uploads (id) ON DELETE SET NULL,
    size_bytes      INTEGER NOT NULL CHECK (size_bytes > 0),
    sha256          TEXT    NOT NULL CHECK (length(sha256) = 64 AND sha256 NOT GLOB '*[^0-9a-f]*'),
    retain_iso      INTEGER NOT NULL CHECK (retain_iso IN (0, 1)),
    iso_blob        TEXT    REFERENCES blobs (sha256) ON DELETE RESTRICT,
    tree_id         TEXT    REFERENCES trees (id) ON DELETE RESTRICT,
    -- WindowsImageDetails / OtherImageDetails, as the API returns them.
    details         TEXT    CHECK (details IS NULL OR (json_valid(details) AND json_type(details) = 'object')),
    warnings        TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(warnings) AND json_type(warnings) = 'array'),
    version         INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL,
    CHECK ((status = 'ready') = (tree_id IS NOT NULL AND kind IS NOT NULL AND details IS NOT NULL)),
    CHECK ((status = 'failed') = (error IS NOT NULL)),
    CHECK (iso_blob IS NULL OR iso_blob = sha256),
    CHECK (status <> 'ready' OR retain_iso = (iso_blob IS NOT NULL))
) STRICT;

-- One live image per ISO: an import of the same ISO points at it instead.
CREATE UNIQUE INDEX images_one_per_iso ON images (sha256) WHERE status <> 'failed';
CREATE INDEX images_kind ON images (kind, id) WHERE kind IS NOT NULL;

CREATE TABLE image_boot_managers (
    image_id     TEXT NOT NULL REFERENCES images (id) ON DELETE CASCADE,
    -- e.g. efi/boot/bootx64.efi, boot.wim:2/Windows/Boot/EFI_EX/bootmgfw_EX.efi
    source_path  TEXT NOT NULL,
    sha256       TEXT NOT NULL REFERENCES blobs (sha256) ON DELETE RESTRICT,
    signer       TEXT NOT NULL CHECK (signer IN ('windows-pca-2011', 'windows-uefi-ca-2023', 'uefi-ca-2011', 'uefi-ca-2023', 'unsigned', 'unknown')),
    architecture TEXT CHECK (architecture IN ('x64', 'arm64')),
    PRIMARY KEY (image_id, source_path)
) STRICT, WITHOUT ROWID;

CREATE INDEX image_boot_managers_blob ON image_boot_managers (sha256);

-- Images now own content (ADR 0015's views).
DROP VIEW tree_refs;
CREATE VIEW tree_refs (tree_id) AS
    SELECT tree_id FROM images WHERE tree_id IS NOT NULL;
DROP VIEW blob_refs;
CREATE VIEW blob_refs (sha256) AS
    SELECT iso_blob FROM images WHERE iso_blob IS NOT NULL
    UNION ALL SELECT sha256 FROM image_boot_managers;
