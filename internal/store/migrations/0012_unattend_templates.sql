-- netdeploy: foreign_keys=off
-- Unattend templates (ADR 0027): the table, profiles.unattend_template_id as
-- a foreign key, and the template and variables each deployment copies.

CREATE TABLE unattend_templates (
    id                 TEXT    PRIMARY KEY,
    name               TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    description        TEXT    NOT NULL DEFAULT '',
    content            TEXT    NOT NULL CHECK (length(CAST(content AS BLOB)) <= 262144),
    identity_modes     TEXT    NOT NULL CHECK (json_valid(identity_modes) AND json_type(identity_modes) = 'array' AND json_array_length(identity_modes) >= 1),
    variable_defaults  TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(variable_defaults) AND json_type(variable_defaults) = 'object'),
    required_variables TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(required_variables) AND json_type(required_variables) = 'array'),
    version            INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at         INTEGER NOT NULL,
    updated_at         INTEGER NOT NULL
) STRICT;

-- profiles: unattend_template_id becomes a foreign key (SQLite's 12-step
-- rebuild). Profiles couldn't name a template before, so every value is NULL
-- (the built-in template).
CREATE TABLE profiles_new (
    id                      TEXT    PRIMARY KEY,
    name                    TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    description             TEXT    NOT NULL DEFAULT '',
    boot_image_id           TEXT    NOT NULL REFERENCES boot_images (id) ON DELETE RESTRICT,
    install_image_id        TEXT    NOT NULL REFERENCES images (id) ON DELETE RESTRICT,
    edition_index           INTEGER NOT NULL CHECK (edition_index >= 1),
    install_method          TEXT    NOT NULL CHECK (install_method IN ('agent', 'smb')),
    -- A foreign key once SMB targets exist.
    smb_target_id           TEXT,
    -- NULL: the built-in template.
    unattend_template_id    TEXT    REFERENCES unattend_templates (id) ON DELETE RESTRICT,
    identity                TEXT    NOT NULL CHECK (json_valid(identity) AND json_extract(identity, '$.mode') IN ('none', 'local', 'ad', 'entra')),
    os_settings             TEXT    CHECK (os_settings IS NULL OR (json_valid(os_settings) AND json_type(os_settings) = 'object')),
    variables               TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(variables) AND json_type(variables) = 'object'),
    boot_order_after_deploy TEXT    NOT NULL CHECK (boot_order_after_deploy IN ('disk-first', 'network-first')),
    version                 INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at              INTEGER NOT NULL,
    updated_at              INTEGER NOT NULL,
    CHECK (install_method <> 'smb' OR smb_target_id IS NOT NULL)
) STRICT;

INSERT INTO profiles_new SELECT id, name, description, boot_image_id, install_image_id, edition_index, install_method,
    smb_target_id, NULL, identity, os_settings, variables, boot_order_after_deploy, version, created_at, updated_at FROM profiles;
DROP TABLE profiles;
ALTER TABLE profiles_new RENAME TO profiles;
CREATE INDEX profiles_boot_image ON profiles (boot_image_id);
CREATE INDEX profiles_install_image ON profiles (install_image_id);
CREATE INDEX profiles_unattend_template ON profiles (unattend_template_id) WHERE unattend_template_id IS NOT NULL;

-- The template (an unattend.TemplateSpec, the built-in one too) and the
-- merged variables a deployment copied when it was created. NULL on
-- deployments from before: they render the built-in template.
ALTER TABLE deployments ADD COLUMN unattend_template TEXT CHECK (unattend_template IS NULL OR (json_valid(unattend_template) AND json_type(unattend_template) = 'object'));
ALTER TABLE deployments ADD COLUMN variables TEXT CHECK (variables IS NULL OR (json_valid(variables) AND json_type(variables) = 'object'));
