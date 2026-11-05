# 12. Our own read-only TFTP server

Status: accepted

## Context
Windows Boot Manager downloads `boot.wim` over TFTP, and asks for the
`windowsize` option (RFC 7440) set by `ramdisktftpwindowsize` in the BCD.
Without it every block waits for its ACK, which makes a 500 MB+ RAM disk slow.

The Phase 1 plan started from `github.com/pin/tftp/v3`. It has no RFC 7440
support: its `SetAnticipate` sends ahead without negotiating, so the client
still runs lock-step. We also need per-client file selection later
([ADR 0006](0006-prebuilt-bcd-per-boot-image.md)), which a library would
have to expose as a hook.

## Decision
`internal/tftp` is our own server: read-only, octet mode, with blksize,
tsize, timeout and windowsize options. Each transfer uses its own socket
(standard TID behaviour). Paths are case-insensitive with `\` normalised to
`/`, and files are opened through `os.Root` so nothing outside the root can
be served. Block numbers roll over to 0 after 65535.

## Consequences
- About 600 lines we maintain, covered by loopback tests (windowing, lost
  ACKs, rollover, tsize probes, path escapes).
- Per-client BCD selection plugs in at path resolution.
- Rollover to 0 (not 1) and windowed transfers still need confirming against
  real Boot Manager in the Phase 1 spike.
- No write support; nothing in the design needs it.
