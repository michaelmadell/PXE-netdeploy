# 23. Our own BCD writer

Status: accepted (2026-11-08). Replaces the tool choice in
[0006](0006-prebuilt-bcd-per-boot-image.md) (`bcdedit` on Windows, `hivex`
on Linux); 0006's other decisions stand.

## Context
Every boot image needs a BCD store: a registry hive (REGF) that tells Boot
Manager to load `\Boot\boot.wim` as a RAM disk over TFTP. ADR 0006 planned
`bcdedit` (needs elevation and a store file) and `hivex` (a C library with
Perl tools) on Linux: two code paths, two tools, and random object GUIDs and
timestamps that make equal inputs give different bytes.

The store we need is small and fixed. Reading the Phase 1 store (made with
`bcdedit`, booted under Secure Boot; `internal/bcd/testdata/phase1.bcd`)
found three objects and sixteen elements (Phase 4 findings): `{bootmgr}`
(description, default, displayorder, timeout), one WinPE OS loader
(device and osdevice `ramdisk=[boot]\Boot\boot.wim,{ramdiskoptions}`, path
`\windows\system32\winload.efi`, systemroot `\windows`, detecthal, winpe)
and `{ramdiskoptions}` (ramdisksdidevice `boot`, ramdisksdipath
`\Boot\boot.sdi`, ramdisktftpblocksize and windowsize). The hive is version
1.3 with `lf` subkey lists and one security descriptor.

## Decision
- `internal/bcd` reads and writes REGF hives in Go: one hive bin, `nk`,
  `vk`, `lf`, value lists, data cells (no big-data cells: nothing in a BCD
  needs them), one shared `sk`, a base block with equal sequence numbers
  and its checksum. No tools, no privileges, the same on every host.
- `bcd.RAMDisk` builds the store with the Phase 1 elements. The loader's
  GUID is fixed (`{6474656e-7065-6f6c-792d-77696e706521}`) and so are the
  timestamps, so a boot image's BCD depends only on its inputs (TFTP block
  and window size).
- The security descriptor gives Administrators read and SYSTEM full access
  like `bcdedit`'s, with Administrators as owner and group (no
  machine-specific SIDs).
- Checks: the written store reads back with our reader as the Phase 1
  store apart from the loader GUID (unit test); `bcdedit /enum all /v` and
  `reg load` accept it (opt-in test, elevated); a lab boot of `nd-win`.
- `/system/capabilities` drops `bcdTool`.

## Consequences
- No `hivex` dependency on Linux, no elevation for BCDs on Windows.
- New BCD settings (e.g. kernel debugging, other loaders) are code changes
  in `internal/bcd`, checked the same way.
- Fallback if firmware ever rejects our hive: ship the Phase 1 store as a
  template and patch its TFTP elements; same interface.
