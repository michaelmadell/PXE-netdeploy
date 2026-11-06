# 6. Pre-built BCD per boot image, selected per client

Status: accepted

## Context
WDS serves each client the boot configuration for its deployment. A BCD store
is a binary registry hive, and there is no reliable pure-Go hive writer.

## Decision
Each boot image build writes its own BCD (`bcdedit` on Windows, `hivex` on
Linux), including TFTP block/window sizes. At boot time the TFTP server maps the
client's IP to its MAC from the DHCP exchange and serves that client's BCD.

## Consequences
- Per-client targeting without generating hives at request time.
- Linux hosts need `hivex` installed; capabilities report it.
- The IP-to-MAC mapping only exists when netdeployd sees the DHCP exchange
  (full or proxy mode; in full mode the lease table also covers restarts).
  With DHCP `off`, every client gets the default content (the default
  profile's boot image). Nothing is ever served at `\BCD`, which Boot Manager
  tries before `\Boot\BCD` and which would bypass per-client selection.
  (Added 2026-11-06, Phase 2 step 4.)
