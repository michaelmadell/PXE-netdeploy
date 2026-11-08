# 20. Backups: scheduled, incremental, to a folder or an SMB share

Status: accepted

## Context
ADR 0014 left a gap: the database can't be copied safely while the server
runs (WAL mode). ADR 0015 added the content store, whose blobs are only
safe to copy with garbage collection paused, after the database snapshot
that references them. Operators want backups to run on a schedule and to
land on a network share, with the share's credentials stored encrypted.
A spike (2026-11-08) ran `cloudsoda/go-smb2` (a maintained fork of
`hirochachacha/go-smb2`, pinned by commit; no tagged releases) against a
Windows 11 share requiring SMB encryption and one not requiring it: NTLM
logon, 256 MiB streamed write and read-back with matching hash (278/399 MB/s
encrypted, 173/264 MB/s plain over loopback), rename replacing an existing
file, `ErrNotExist` on missing files, and a clear error for a bad password.

## Decision
- **What a backup holds**: a `VACUUM INTO` snapshot of the database, the
  admin certificate and key, and every blob in `content/sha256/` the
  snapshot references. Not `uploads/` or `staging/` (temporary), and not the
  secrets key (ADR 0021): a backup never carries the key to its own secrets.
- **Layout** at the destination:
  `sha256/ab/<hash>` shared by all backups, and
  `snapshots/<UTC time, e.g. 20261108T020000Z>/` with `netdeploy.db`,
  `admin-cert.pem`, `admin-key.pem` and `manifest.json` (server and schema
  version, trigger, blob count and bytes, the secrets key's fingerprint).
  A snapshot directory is written as `<name>.partial` and renamed when
  complete; listing ignores `.partial` ones and the next run removes them.
- **Incremental**: blobs never change, so a run copies only blobs missing at
  the destination (one listing per `sha256/xx` directory). Every blob is
  re-hashed as it is read from the live store, so a backup also detects a
  damaged store (the backup fails, naming the blob, and `storage` health
  goes `error`).
- **Consistency**: a backup is a job (`system.backup`, ADR 0016) on its own
  one-at-a-time lane. It holds the content store's sweep, then takes the
  snapshot and copies; a sweep asked for meanwhile (after a delete) is
  deferred until the backup ends, so deletes never wait. Imports continue;
  their blobs simply aren't in this snapshot. In the snapshot copy, the
  backup's own job is marked succeeded and recorded as the last success,
  so a restored server doesn't see it as interrupted.
- **Schema change**: adding the job kind rebuilds `jobs` (SQLite's 12-step
  procedure). Migrations can now start with `-- netdeploy: foreign_keys=off`
  to run without foreign-key enforcement, with `foreign_key_check` before
  commit; Phase 4's `clients` rebuild uses the same.
- **Retention**: `backup.retention` snapshots are kept (default 5; `0`
  keeps all). After a successful run, older snapshots are deleted, then blobs
  no remaining snapshot references (read from their databases).
- **Schedule** (`backup.schedule`): `disabled`, `hourly` (at `minute`),
  `daily` (at `time`), `weekly` (`daysOfWeek` at `time`), `monthly`
  (`dayOfMonth` at `time`; months shorter than `dayOfMonth` run on their
  last day). Times are the server's local time: a time skipped by a clock
  change runs at the next valid moment, a repeated one runs once. If the
  server was down at a scheduled time, one backup runs as soon as it starts.
  `POST /system/backups` runs one now, whatever the schedule.
- **Destinations** (`backup.destination`): `local` (an absolute path, not
  inside the data or content directories) or `smb` (`\\server\share\path`
  and a `share-credential` secret). SMB is spoken by a pure-Go client
  (`cloudsoda/go-smb2`, SMB 2/3 with signing and encryption) rather than an
  OS mount: same behaviour on Windows and Linux, no root, no dependence on
  the service account's network logon.
- **Visibility**: `GET /system/backups` lists the snapshots at the
  destination; `GET /system/health` reports the last success, last failure
  and next run, with `backup_failed` and `backup_overdue` warnings.
- **Offline commands**: `netdeployd backup -data … -to <path|UNC>` (refuses
  while a server holds the data directory's lock, an OS file lock on
  `<data>/netdeployd.lock` that the server, `restore` and `key import` also
  take) and
  `netdeployd restore -from <snapshot path|UNC> -data … [-content …]`,
  which refuses a non-empty data directory without `-force`, copies the
  snapshot's blobs, verifies every referenced blob is present, and lists
  secrets it can't decrypt (ADR 0021). UNC paths take `-user` and prompt for
  the password.

## Consequences
- Restore is offline only; restoring into a running server isn't supported.
- A destination problem doesn't block saving settings; it shows on the next
  run (`POST /system/backups` doubles as a test) and in health.
- After the first full copy, scheduled backups cost about the new content
  plus the database.
- Settings now reference a secret, so a secret in use by the backup
  destination can't be deleted (`secret_in_use`).
