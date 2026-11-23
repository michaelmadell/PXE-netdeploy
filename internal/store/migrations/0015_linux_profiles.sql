-- netdeploy: foreign_keys=off
-- Linux profiles (ADR 0034): profiles.kind; the Windows columns become
-- nullable, required for 'windows' and absent for 'linux'; kernel_args for
-- 'linux'. SQLite's 12-step rebuild; every existing profile is Windows.

CREATE TABLE profiles_new (
    id                      TEXT    PRIMARY KEY,
    name                    TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    description             TEXT    NOT NULL DEFAULT '',
    kind                    TEXT    NOT NULL DEFAULT 'windows' CHECK (kind IN ('windows', 'linux')),
    boot_image_id           TEXT    REFERENCES boot_images (id) ON DELETE RESTRICT,
    install_image_id        TEXT    NOT NULL REFERENCES images (id) ON DELETE RESTRICT,
    edition_index           INTEGER CHECK (edition_index >= 1),
    install_method          TEXT    CHECK (install_method IN ('agent', 'smb')),
    -- A foreign key once SMB targets exist.
    smb_target_id           TEXT,
    -- NULL: the built-in template.
    unattend_template_id    TEXT    REFERENCES unattend_templates (id) ON DELETE RESTRICT,
    identity                TEXT    CHECK (identity IS NULL OR (json_valid(identity) AND json_extract(identity, '$.mode') IN ('none', 'local', 'ad', 'entra'))),
    os_settings             TEXT    CHECK (os_settings IS NULL OR (json_valid(os_settings) AND json_type(os_settings) = 'object')),
    variables               TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(variables) AND json_type(variables) = 'object'),
    boot_order_after_deploy TEXT    NOT NULL CHECK (boot_order_after_deploy IN ('disk-first', 'network-first')),
    kernel_args             TEXT    NOT NULL DEFAULT '' CHECK (length(kernel_args) <= 1024 AND instr(kernel_args, char(10)) = 0 AND instr(kernel_args, char(13)) = 0 AND instr(kernel_args, '"') = 0),
    version                 INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at              INTEGER NOT NULL,
    updated_at              INTEGER NOT NULL,
    CHECK (install_method IS NOT 'smb' OR smb_target_id IS NOT NULL),
    CHECK (kind <> 'windows' OR (boot_image_id IS NOT NULL AND edition_index IS NOT NULL AND install_method IS NOT NULL AND identity IS NOT NULL AND kernel_args = '')),
    CHECK (kind <> 'linux' OR (boot_image_id IS NULL AND edition_index IS NULL AND install_method IS NULL AND identity IS NULL
        AND smb_target_id IS NULL AND unattend_template_id IS NULL AND os_settings IS NULL))
) STRICT;

INSERT INTO profiles_new (id, name, description, kind, boot_image_id, install_image_id, edition_index, install_method,
    smb_target_id, unattend_template_id, identity, os_settings, variables, boot_order_after_deploy, kernel_args, version, created_at, updated_at)
    SELECT id, name, description, 'windows', boot_image_id, install_image_id, edition_index, install_method,
    smb_target_id, unattend_template_id, identity, os_settings, variables, boot_order_after_deploy, '', version, created_at, updated_at FROM profiles;
DROP TABLE profiles;
ALTER TABLE profiles_new RENAME TO profiles;
CREATE INDEX profiles_boot_image ON profiles (boot_image_id) WHERE boot_image_id IS NOT NULL;
CREATE INDEX profiles_install_image ON profiles (install_image_id);
CREATE INDEX profiles_unattend_template ON profiles (unattend_template_id) WHERE unattend_template_id IS NOT NULL;
