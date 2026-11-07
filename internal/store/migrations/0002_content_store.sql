-- Content store (ADR 0015). Blobs are files under <content>/sha256/, named by
-- their hash; these rows say which exist and what refers to them.

CREATE TABLE blobs (
    sha256     TEXT    PRIMARY KEY CHECK (length(sha256) = 64 AND sha256 NOT GLOB '*[^0-9a-f]*'),
    size       INTEGER NOT NULL CHECK (size >= 0),
    created_at INTEGER NOT NULL
) STRICT, WITHOUT ROWID;

-- An immutable manifest: an extracted ISO, a driver package, a boot image.
CREATE TABLE trees (
    id          TEXT    PRIMARY KEY,
    kind        TEXT    NOT NULL CHECK (kind IN ('image', 'driver-package', 'boot-image')),
    file_count  INTEGER NOT NULL CHECK (file_count >= 0),
    total_bytes INTEGER NOT NULL CHECK (total_bytes >= 0),
    created_at  INTEGER NOT NULL
) STRICT;

-- path: as on the media, '/'-separated, no leading '/', no '.', '..', ':',
-- '\' or NUL. fold: lower(path), the case-insensitive lookup key, so two
-- paths differing only in case can't be in one tree.
CREATE TABLE tree_entries (
    tree_id TEXT NOT NULL REFERENCES trees (id) ON DELETE CASCADE,
    path    TEXT NOT NULL CHECK (path <> '' AND substr(path, 1, 1) <> '/' AND instr(path, '\') = 0
                                 AND instr(path, ':') = 0 AND instr(path, char(0)) = 0
                                 AND '/' || path || '/' NOT LIKE '%/../%' AND '/' || path || '/' NOT LIKE '%/./%'
                                 AND instr(path, '//') = 0 AND substr(path, -1, 1) <> '/'),
    fold    TEXT NOT NULL CHECK (fold = lower(path)),
    sha256  TEXT NOT NULL REFERENCES blobs (sha256) ON DELETE RESTRICT,
    PRIMARY KEY (tree_id, fold)
) STRICT, WITHOUT ROWID;

CREATE INDEX tree_entries_blob ON tree_entries (sha256);

-- What keeps trees and blobs alive. Garbage collection deletes trees not in
-- tree_refs, then blobs in neither tree_entries nor blob_refs. Migrations
-- that add owners (images, driver packages, boot images) redefine these
-- views; owner columns also reference trees/blobs with ON DELETE RESTRICT.
CREATE VIEW tree_refs (tree_id) AS SELECT NULL WHERE 0;
CREATE VIEW blob_refs (sha256) AS SELECT NULL WHERE 0;
