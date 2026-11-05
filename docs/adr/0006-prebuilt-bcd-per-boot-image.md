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
