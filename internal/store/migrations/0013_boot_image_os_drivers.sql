-- Whether a boot image's served build can add OS drivers during a deployment
-- (ADR 0022 amendment): 1 when the built WinPE has the WinPE-Setup package,
-- 0 when it doesn't, NULL until built or for builds made before this column.

ALTER TABLE boot_images ADD COLUMN can_add_os_drivers INTEGER CHECK (can_add_os_drivers IN (0, 1));
