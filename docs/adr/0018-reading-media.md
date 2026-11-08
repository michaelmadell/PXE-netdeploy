# 18. Reading install media: own UDF and WIM metadata readers, go-wim, go-uefi

Status: accepted

## Context
Image import (Phase 3) needs, from a Windows ISO: every file (stored in the
content store, ADR 0015), the editions and boot images inside the WIMs, and
every boot manager with its Secure Boot signer, including the 2023-signed
`bootmgfw_EX.efi` that only exists inside `boot.wim` (Phase 1). It must work
on Windows and Linux hosts, without privileges (no mounting) and without
external tools. A spike against current Win11 media (24H2/25H2 family,
`CPBA_X64FRE_EN-GB_DV9`, 2026-11-08) established:

- The ISO is a UDF 1.02 bridge; the ISO9660 side is a stub. One type-1
  partition, File Entries only, short allocation descriptors only; most files
  are one extent, `install.wim` (8.0 GB) is eight (1 GiB extent limit).
- `boot.wim` and `install.wim` are LZX with 32 KiB chunks.
- `github.com/Microsoft/go-winio/wim` reads the header but fails on the
  directory tree of current media.
- `github.com/nradchenko/go-wim` (MIT, no dependencies, LZX) extracts
  `bootmgfw_EX.efi` from `boot.wim` read in place from the ISO, with the
  expected hash, in ~90 ms.
- `github.com/ejfkdev/udf` works but pulls ~150 modules into the build and
  its files can't be read at an offset; `mogaika/udf` is unmaintained.

## Decision
- **Own read-only UDF reader** (`internal/media/udf`): UDF 1.02–2.01, 2048-byte
  sectors, one type-1 partition, File Entry and Extended File Entry, short,
  long and inline allocation descriptors, 8- and 16-bit d-strings. Metadata
  partitions (UDF 2.50), virtual/sparable maps and multi-session are refused
  (`unsupported_media`). It exposes a case-insensitive `fs.FS` whose files
  implement `io.ReaderAt` across their extents.
- **Plain ISO9660 media** (no UDF) is refused until the Linux phase.
- **WIM metadata** (images, editions, languages, builds, sizes) comes from
  the WIM's XML resource with our own small reader; it is stored
  uncompressed in WIM, ESD and split WIM alike, so all install formats work.
- **Files inside WIMs** (boot managers from `boot.wim`) are read with
  `go-wim` behind an internal interface. A `boot.wim` it can't read (not
  LZX) gives a warning on the image, not a failed import. `wimlib-imagex`
  (already required by ADR 0005) is the fallback if `go-wim` ever has to go.
  The import never decompresses the install image.
- **Signers** come from the embedded Authenticode signature, parsed and
  digest-checked with `github.com/foxboron/go-uefi/authenticode`, and
  classified against the four Microsoft CA certificates compiled into the
  binary: Windows Production PCA 2011, Windows UEFI CA 2023, Microsoft
  Corporation UEFI CA 2011, Microsoft UEFI CA 2023. No signature is
  `unsigned`; a digest mismatch or any other chain is `unknown`.
- **Import** extracts every file into a content store tree, records the
  original file name, volume label and SHA-256 of the ISO, and stores each
  boot manager (on the media and inside each `boot.wim` image) as its own
  blob with its signer. An imported image doesn't need its ISO: the upload
  can be deleted at any time.

## Consequences
- No privileges, mounts or external tools for import, on either host OS.
- Two new dependencies (`go-wim`, `go-uefi`); `go-wim` is young, so it stays
  behind an interface with a known fallback.
- An imported ISO costs its size again in the content store; while its
  upload is also kept (ADR 0017) the total is about twice the ISO.
- Linux and other non-UDF media need an ISO9660 reader in the Linux phase.
