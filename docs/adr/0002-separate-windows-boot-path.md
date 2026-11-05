# 2. Windows boots without a shim

Status: accepted

## Context
Many machines, notably Secured-core PCs, trust only the Microsoft Windows CA and
not the third-party UEFI CA that signs shim and iPXE. A Windows path that goes
through shim or iPXE fails on them.

## Decision
Windows clients boot Microsoft's own signed `bootmgfw.efi` directly over TFTP
(or inside an HTTP Boot ISO). Shim and iPXE are used only for non-Windows
operating systems, in a later phase.

Image import records every boot manager on the media with its signer; boot
images serve the 2023-signed one when present, else the 2011-signed one.

## Consequences
- Windows deployment works on stock Secure Boot hardware with no key changes.
- Boot-time menus for Windows come from BCD selection, not iPXE.
- The lab must test both Secure Boot templates (Windows-only and third-party CA).
