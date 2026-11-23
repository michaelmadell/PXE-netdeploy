# 33. Reading Linux media: ISO9660, distribution families, every signer

Status: accepted (2026-11-23). Phase 7 decision 2. Amends
[0018](0018-reading-media.md) (plain ISO9660 was refused "until the Linux
phase"). Builds on [0015](0015-content-addressed-store.md) and the Phase 7
spike ([findings](../spikes/phase-7-findings.md)).

## Context
The spike booted Ubuntu 26.04.1 and 24.04.5 installers under Secure Boot
through the bundled iPXE chain: iPXE's `shim` command with the
distribution's own shim, then the kernel and initrd with our command line,
then casper downloading **the whole ISO** (`url=`, plain HTTP: its initramfs
has no TLS client). A network boot of a Linux image therefore needs, from
import: the shim with all its signatures (Ubuntu's shims are signed by the
2011 UEFI CA only, the iPXE shim by 2011 and 2023), the kernel and initrd
paths, and for Ubuntu the ISO itself. Other families differ: Anaconda wants
the extracted tree (`inst.repo=`), debian-installer a mirror.

Linux ISOs are ISO9660 with Rock Ridge (and usually Joliet), no UDF, so
import refused them with `unsupported_media`.

## Decision
- **Own read-only ISO9660 reader** (`internal/media/iso9660`), shaped like
  the UDF reader: 2048-byte sectors, the primary volume descriptor,
  **Rock Ridge** names (SUSP `NM`, `PX`, `SL`, `CE` continuations), else
  **Joliet** (supplementary descriptor, UCS-2), else plain names with the
  `;1` version and a trailing `.` removed; multi-extent files (over 4 GB);
  a case-insensitive `fs.FS` whose files implement `io.ReaderAt`. Symlinks
  are reported as symlinks and not followed; import copies regular files
  only. El Torito isn't parsed: every target family also has
  `EFI/BOOT/BOOTX64.EFI` (or `BOOTAA64.EFI`) as a file.
- **Import order:** UDF if the media has it (Windows), else ISO9660; neither
  is `unsupported_media`.
- **Families**, detected in this order; the first match wins, no match is
  `kind: other` as before:

  | Family | Recognised by | Version, architecture |
  |---|---|---|
  | `ubuntu` (casper, Subiquity) | `casper/vmlinuz` and `casper/initrd`, `.disk/info` | `.disk/info`; architecture from it, else from the shim's name |
  | `debian` (debian-installer) | `install.amd/` or `install.a64/` with `vmlinuz` and `initrd.gz`; on live media `install/` with them and a `.disk/info` naming Debian (the live system's `live/` is ignored) | `.disk/info`; the directory gives the architecture, else `.disk/info`, else the shim's name |
  | `redhat` (Anaconda: Fedora, RHEL, Rocky, Alma, CentOS Stream) | `.treeinfo` with an `[images-<arch>]` section naming `kernel` and `initrd`; else (boot ISOs have no `.treeinfo`) `.discinfo` with `images/pxeboot/vmlinuz` and `initrd.img` | `.treeinfo` `[general]` (or `[release]`/`[tree]`); for boot ISOs `.discinfo` (version, architecture) and the name from GRUB's first "Install <name> <version>" entry |

- **Only `ubuntu` is deployable** (`details.deployable`) until a family is
  proven by a spike; the others are imported and recorded, and profile
  validation refuses them with `deployableReason`.
- **`LinuxImageDetails`**: family, distribution, version, architecture, the
  media's own description, and `boot.shim`, `boot.grub`, `boot.kernel`
  (each with path, SHA-256 and signers) and `boot.initrd` (path, SHA-256).
- **Every signer.** `BootManager` gains `signers` (every Microsoft CA any of
  its signatures chains to) and `signedBy` (the subject of each verified
  signing certificate, for distribution keys such as Canonical's). `signer`
  keeps its meaning (the first match), so Windows images read as before; the
  classifier gains an all-matches mode. Windows imports fill the new fields
  too.
- **Warnings** (the image still imports): no shim (no Secure Boot path); a
  shim signed by the 2011 UEFI CA only ("machines that trust only the 2023
  CAs refuse it"); a family recognised but its kernel or initrd missing
  (then `kind: other`).
- **Storage:** every image's full tree goes into the content store as today.
  Ubuntu images also keep the ISO as a blob (`retainIso` forced on, hard-
  linked from the upload where the volume allows), because casper downloads
  it whole. Serving it to clients belongs to decision 3.

## Consequences
- Linux ISOs import on any host without privileges or tools, as Windows
  media does.
- An Ubuntu image costs its ISO's size twice in the content store (about
  5.6 GB for 26.04.1), nothing extra while the upload is kept (hard link).
  The tree keeps import uniform and is what Anaconda will need.
- Debian and Red Hat media show up correctly in the UI before they can be
  deployed; making them deployable means a spike (boot path, automation),
  then flipping `deployable` and adding their checks.
- The ISO9660 reader is new code on untrusted input: bounds checks on every
  record, loop limits on `CE` chains and directory depth, and a Go fuzz
  test.
- Amended 2026-11-23, after real media: Debian Live 13.8.0 and the RHEL
  10.3 boot ISO first imported as `other` (live media keeps the installer
  in `install/`; boot ISOs have `.discinfo`, not `.treeinfo`). Both rules
  above now cover them. Their shims are signed by **both** Microsoft UEFI
  CAs (RHEL's also by Red Hat's own key), unlike Ubuntu's.
