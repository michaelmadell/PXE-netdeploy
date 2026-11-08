# 17. Uploads: our own minimal tus server

Status: accepted

## Context
ISOs (up to tens of GB) and driver archives reach the server as uploads; the
spec routes them through tus 1.0.0 so the UI (tus-js-client) can resume.
Phase 2's request validator (kin-openapi) reads a request body fully before
validating it, and the generated strict handlers expect JSON, so upload
requests can't go through either. tusd (the reference server) brings its own
file layout, lock files, storage plug-ins and many dependencies, and knows
nothing of our content store (ADR 0015), database or disk space checks.

## Decision
- **Own server** for tus 1.0.0 core plus the creation, termination and
  expiration extensions (no checksum, concatenation or deferred length). A
  plain `http.Handler` serves `/api/v1/uploads…` ahead of the validator and
  idempotency middleware, on the admin listener only. `OPTIONS /uploads`
  advertises the version, extensions and maximum size. JSON `GET /uploads`
  and `GET /uploads/{id}` list uploads for the UI.
- **Files** live in `<content>/uploads/<id>` (same volume as the blobs);
  rows in an `uploads` table. The SHA-256 is computed as bytes arrive and its
  state saved with the offset, so a completed upload's hash is known without
  re-reading it (an import of media already stored finishes at once).
- **Checkpoints**: every 64 MiB within a request, and when it ends (including
  a dropped connection), the file is fsynced and the offset and hash state
  saved. At startup incomplete files are truncated to their saved offset;
  files without rows and rows without files are removed.
- **One writer per upload**: a new `PATCH` for an upload still being written
  stops the old request (almost always a dead connection), waits for its
  checkpoint, then checks `Upload-Offset` (`409 upload_offset_mismatch`).
- **Limits**: 64 GiB per upload (above a 50 GB dual-layer Blu-ray image),
  `413 upload_too_large` beyond. Creation must fit the reserve plus the bytes
  still to come for other incomplete uploads (`507 insufficient_storage`);
  free space is re-checked every 256 MiB while receiving.
- **Lifecycle**:
  - An import claims a complete upload in the same transaction that queues
    its job (`409 upload_incomplete`, `409 upload_in_use`).
  - A **successful** import marks the upload `imported`: it is kept until
    deleted (`DELETE /uploads/{id}`), never expired. `retainIso` hardlinks the
    file into the content store (a copy where hardlinks aren't supported), so
    the upload and the image's ISO don't cost twice.
  - A **failed** import releases the upload and restarts its expiry; the
    job's error says until when the upload is kept for a retry.
  - Uploads never imported expire `storage.uploadExpiryHours` (default 24)
    after their last activity, unless claimed by a queued or running job.
    Responses carry `Upload-Expires`.

## Consequences
- No new dependencies; the protocol surface is small and tested directly
  with a tus client's request sequence.
- Imported uploads accumulate until deleted; their total is reported in
  `Health.storage.uploadBytes` and counts against free space.
- A blob that is a hardlink of a kept upload stays when the upload is
  deleted, and vice versa.
- Upload bodies bypass the request validator; the handler validates tus
  headers itself.
