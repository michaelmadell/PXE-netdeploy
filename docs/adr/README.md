# Architecture decision records

Short records of significant decisions: context, decision, consequences.
Never deleted; a reversed decision gets a new ADR that supersedes the old one.

| # | Title | Status |
|---|---|---|
| [0001](0001-record-architecture-decisions.md) | Record architecture decisions | accepted |
| [0002](0002-separate-windows-boot-path.md) | Windows boots without a shim | accepted |
| [0003](0003-go-for-server-and-agent.md) | Go for the server and the WinPE agent | accepted |
| [0004](0004-spec-first-openapi-3.0.md) | Spec-first API with OpenAPI 3.0.3 | accepted |
| [0005](0005-boot-image-builders.md) | Two boot image builders: DISM and wimlib | accepted |
| [0006](0006-prebuilt-bcd-per-boot-image.md) | Pre-built BCD per boot image, selected per client | accepted |
| [0007](0007-agent-and-smb-install-methods.md) | Agent install by default, SMB optional | accepted |
| [0008](0008-secrets-model.md) | Secrets are write-only and only decrypted for an agent | accepted |
| [0009](0009-separate-admin-and-content-listeners.md) | Separate admin and content listeners | accepted |
| [0010](0010-unknown-clients-ignored-by-default.md) | Unknown clients are ignored by default | superseded by 0013 |
| [0011](0011-frontend-stack.md) | Vite + React + Tailwind v4 + shadcn/ui | accepted |
| [0012](0012-own-tftp-server.md) | Our own read-only TFTP server | accepted |
| [0013](0013-boot-prompt-password-gate.md) | Every PXE client boots WinPE; a password gate prevents re-imaging | accepted |
| [0014](0014-sqlite-store.md) | SQLite store with hand-written SQL and embedded migrations | accepted |
| [0015](0015-content-addressed-store.md) | Content-addressed store for imported and built files | accepted |
| [0016](0016-jobs.md) | Jobs: rows as the queue, an in-process runner, no resume | accepted |
| [0017](0017-uploads-own-tus-server.md) | Uploads: our own minimal tus server | accepted |
| [0018](0018-reading-media.md) | Reading install media: own UDF and WIM metadata readers, go-wim, go-uefi | accepted |
| [0019](0019-driver-packages.md) | Driver packages: archives, INF parsing, catalog verification | accepted |
| [0020](0020-backups.md) | Backups: scheduled, incremental, to a folder or an SMB share | accepted |
| [0021](0021-secrets-storage.md) | Secrets storage: one data key, protected by DPAPI or a key file | accepted |
| [0022](0022-building-boot-images.md) | Building boot images: pipeline, inputs, caching, cleanup | accepted |
| [0023](0023-own-bcd-writer.md) | Our own BCD writer (replaces bcdedit/hivex from 0006) | accepted |

New ADR: copy the shape of an existing one, take the next number, add it here.
