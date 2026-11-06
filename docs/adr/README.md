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

New ADR: copy the shape of an existing one, take the next number, add it here.
