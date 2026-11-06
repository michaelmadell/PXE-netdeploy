# 14. SQLite store with hand-written SQL and embedded migrations

Status: accepted

## Context
Phase 2 needs persisted settings, clients, deployments (ADR 0013 reads them on
every PXE boot), DHCP leases and idempotency keys. ADR 0003 already chose
SQLite via `modernc.org/sqlite` (no cgo). This records how it is used.

## Decision
- **One file**, `<data>/netdeploy.db`, in WAL mode with `foreign_keys=ON` and
  `busy_timeout=5000`. A writer pool of one connection (transactions start
  `BEGIN IMMEDIATE`) and a small read-only pool.
- **The schema enforces the invariants**: `STRICT` tables, `CHECK` constraints
  for enums and JSON validity, `UNIQUE` on the client MAC and reserved IP, and a
  partial unique index allowing one `pending`/`running` deployment per client.
- **Timestamps** are integer Unix milliseconds (UTC); IDs are ULIDs.
- **Settings is one JSON document** in a single-row table with a `version`
  counter. `PUT /settings` already replaces the document atomically, so tables
  would add joins without adding integrity. The ETag is the version; writes are
  a compare-and-swap on it.
- **Migrations** are numbered SQL files embedded in the binary, applied at
  startup in order, one transaction each, tracked in `PRAGMA user_version`.
  Forward only: before migrating a non-empty database the server writes a
  `VACUUM INTO` snapshot next to it, which is the rollback. The server refuses
  to start on a database newer than it knows.
- **Hand-written SQL** with `database/sql` in `internal/store`; no ORM, no
  sqlc.
- **Kept in memory, not stored:** DHCP offers, boot sessions (IP to MAC), the
  `/agent/unlock` rate limiter, and the `lastSeenAt` write throttle.

## Consequences
- One file to back up, but copying it while the server runs is not safe in WAL
  mode; a backup endpoint or documented procedure is needed before Phase 3.
- SQLite can't add a foreign key to an existing table, so the Phase 4 migration
  that adds `profiles` rebuilds `clients` (SQLite's 12-step procedure). Until
  then `clients.profile_id` is checked by the application.
- Settings fields aren't queryable in SQL; nothing needs them to be.
- Recording a sighting (`lastSeenAt` etc.) doesn't change a client's version,
  so a booting machine never invalidates an admin's `If-Match`.
- Store tests use real SQLite in a temp directory, no mocks.
- TLS private keys stay as files in the data directory until secrets
  (ADR 0008) arrive in Phase 5; the boot prompt password is stored as an
  argon2id hash inside the settings document.
