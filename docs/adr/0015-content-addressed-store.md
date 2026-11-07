# 15. Content-addressed store for imported and built files

Status: accepted

## Context
Phase 3 imports ISOs (several GB each) and driver packs; Phase 4 builds boot
images from them. TFTP and content HTTP serve a boot image as a directory tree
per client (ADR 0006, Phase 2 step 4), and the agent downloads install images.
`architecture.md` already says "content-addressed files (SHA-256)". This records
the layout, how trees are described and served, how space is reclaimed, and
how disk space is protected.

## Decision
- **Location.** The content root defaults to `<data>/content`; the `-content`
  startup flag overrides it. It is not a stored setting: moving a store of
  large files can't be applied atomically by `PUT /settings`.
- **Layout.** `sha256/ab/<64 hex>` holds blobs, named by their hash and never
  modified. `staging/<jobId>/` holds files a running job is writing; it is
  emptied at startup. `uploads/` holds partial tus uploads. All three are on
  one volume (checked at startup with a rename), so publishing a file is an
  atomic rename and a retained ISO moves from `uploads/` into `sha256/` without
  a copy.
- **Writing.** A job streams each file into staging while hashing it and
  fsyncs it. At commit, under the store's lock, each file is renamed into
  `sha256/` unless that blob already exists (then the staged copy is removed),
  and the rows are inserted in one transaction. A crash between the rename and
  the transaction leaves a file without a row, which the orphan scan removes.
- **Trees.** A tree (an extracted ISO, a driver package, a boot image) is a
  manifest in SQLite: `trees` and `tree_entries(tree_id, path, fold, sha256)`
  with the primary key on `(tree_id, fold)`, where `fold` is the lower-cased
  path. Paths are `/`-separated (`\` is converted), with no `..`, `:`, NUL or
  leading `/`. Trees are immutable; owners point at them (`images.tree_id`, and
  later driver packages and boot images) with `ON DELETE RESTRICT`. Single
  files (a retained ISO) reference `blobs` directly. Directories are implied.
- **Serving.** A tree is served as a virtual `fs.FS`: a path is looked up in
  the manifest (case-insensitively, through `fold`) and the blob file is
  opened, so `http.ServeContent` and TFTP read a real `*os.File`.
  `boot.Answer.Root` becomes an `fs.FS`; a directory adapter keeps the
  temporary `-tftp-root` flag working until Phase 4. TFTP still never serves
  `\BCD`. Builders that need a real folder (DISM `/Add-Driver`) get a
  materialised copy (hardlinks where possible, else copies) in Phase 4.
- **Garbage collection** is mark and sweep, not reference counts: the
  referencing rows are the truth. Under the store's lock a sweep deletes blob
  rows nothing references, deletes their files, then removes any file in
  `sha256/` without a row (this also retries deletes Windows refused because a
  file was open). It runs at startup, after deletes and after failed jobs.
- **Startup check.** Every blob row is checked for existence and size; a
  missing or truncated blob makes the `storage` health service `error`. Hashes
  are verified when written, not re-verified at startup.
- **Disk space.** `storage.reserveBytes` (setting, default 5 GiB) is space the
  store never uses. Upload creation and import requests are refused with `507`
  (`insufficient_storage`) when they wouldn't fit; jobs check free space every
  256 MiB written and fail with `insufficient_storage` before the disk fills.
  `GET /system/health` reports the store's space and a `storage_low` warning.
- **No secrets.** Rendered unattend files, ODJ blobs and other secret material
  never enter the content store (ADR 0008); anything in it may be served.

## Consequences
- Identical files are stored once across images, driver packages and boot
  images (boot.sdi, fonts and unchanged media files in particular).
- The TFTP and content handlers change from `os.Root` directories to `fs.FS`
  in Phase 3, and the case-insensitive directory walk in TFTP becomes an
  indexed lookup for stored trees.
- Deduplication is per whole file; WIMs already deduplicate internally, so
  chunking isn't worth it.
- Backups need the database first (`VACUUM INTO`), then `sha256/`, with
  garbage collection paused; a documented procedure and CLI command come at the
  end of Phase 3.
- Full re-hashing of the store is not done automatically; if needed it becomes
  an on-demand job.
- The 5 GiB default reserve is part of the host's minimum disk requirement.
