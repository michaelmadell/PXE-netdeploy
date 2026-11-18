# 22. Building boot images

Status: accepted (2026-11-08; amended the same day after the lab builds:
`auto` prefers wimlib, storage drivers allowed with wimlib; amended
2026-11-18: the Setup image by default, see the end). Refines
[0005](0005-boot-image-builders.md).

## Context
A boot image is WinPE from a Windows image's `boot.wim` plus our agent, its
configuration and (for hardware such as the Intel I210) drivers, together
with the files Boot Manager loads over TFTP. ADR 0005 chose two builders,
DISM and wimlib; this records how a build works.

A read-only probe of the Phase 3 ISO (`CPBA_X64FRE_EN-GB_DV9`, build
26100.9457, 2026-11-08) found:

- Each `boot.wim` index holds every boot file: both boot managers
  (`Windows/Boot/EFI/bootmgfw.efi`, 2011; `Windows/Boot/EFI_EX/bootmgfw_EX.efi`,
  2023), `Windows/Boot/DVD/EFI/boot.sdi` (same size as the media's) and two
  font sets, `Windows/Boot/Fonts/*.ttf` and `Windows/Boot/Fonts_EX/*_EX.ttf`
  (for the 2023 boot manager).
- Neither index has a `winpeshl.ini`. Index 2 (Windows Setup) has `setup.exe`
  and `sources/setup.exe`, the program that showed the WDS deprecation block
  in Phase 1; index 1 (plain WinPE) has neither. Expanded sizes: 2.45 GB and
  2.56 GB; both have `drvload.exe` and `wpeinit.exe`.
- DISM is part of Windows (no ADK needed to export, mount and add drivers)
  but refuses to run unelevated (Phase 3).

## Decision
- **Pipeline** (one `bootImage.build` job on the `build` lane, ADR 0016):
  1. Resolve and check the inputs (below).
  2. Export only the chosen index of the source `boot.wim` (read from its
     content-store blob) into a new LZX WIM in the job's staging directory.
  3. Add our files: **dism** mounts it, runs `/Add-Driver` per enabled INF,
     copies the files in, commits, then exports again into a fresh WIM to
     drop the commit's dead space. **wimlib** runs `wimlib-imagex update
     --rebuild` and stages the driver files for `drvload`.
  4. Read the boot manager, `boot.sdi` and fonts out of the built WIM.
  5. Write the BCD (writer chosen in decision 2 of Phase 4).
  6. Commit the tree through `cas` staging and attach it to the boot image.
- **Builder choice**: `auto` uses wimlib when `wimlib-imagex` is found (PATH
  or `-wimlib`); otherwise DISM when `dism.exe` exists, the server is
  elevated and DISM's build is not older than the image's; otherwise
  `422 builder_unavailable`. wimlib first because the lab builds showed its
  customise step at 1.3 s against DISM's 43 s, it needs no elevation, and
  (below) it handles every driver the boot image needs. `builder: dism`
  still forces DISM, whose offline injection lets drivers load at boot
  without the agent. The Windows ADK is **not** a prerequisite (it is only
  needed for optional components, deferred). wimlib is required on Linux
  (package `wimtools`) and recommended on Windows; it is not bundled
  (licence to check before Phase 6 packaging). A Windows server without it
  uses the built-in DISM when elevated.
- **Storage drivers** (e.g. Dell PERC, Intel RST for VMD RAID): WinPE runs
  from a RAM disk that Boot Manager downloads, so no WinPE driver is
  boot-critical; disks behind the controller are data disks until the
  driver loads. With wimlib the agent loads every staged driver with
  `drvload` before it enumerates disks (as Windows Setup's "Load driver"
  does) and waits for the disk list to settle. This reverses ADR 0005's
  "not suitable for boot-critical storage drivers": that concern belongs to
  the **installed** Windows, which must get its storage driver offline
  before its first boot (the agent's OS driver step, Phase 4 decision 4),
  whichever builder made the boot image. Drivers whose `.sys` needs a
  reboot can't be loaded with `drvload` (Microsoft's documentation); to
  verify with PERC and RST VMD on real hardware.
- **BCD**: written by `internal/bcd` (ADR 0023) behind an interface.
- **What goes into WinPE**: `X:\netdeploy\agent.exe`,
  `X:\netdeploy\config.json` (server URLs, boot image id, input hash, staged
  driver INFs; a place for the Phase 6 content CA), `X:\netdeploy\drivers\`
  (wimlib builds only) and `Windows\System32\winpeshl.ini` launching only
  the agent. The agent loads staged drivers with `drvload`, then runs
  `wpeinit`, so a NIC driver is present before networking starts. The agent
  binaries (windows/amd64, windows/arm64) are built with the server and
  embedded in it (`-tags agent`; `just build` does this), or read from
  `-agent-dir` (default `./dist`) by a binary built without them.
  `agentVersion` accepts only that bundled version for now.
- **The built tree** (served per client, ADR 0006):
  `bootmgfw.efi` (the boot manager chosen by `bootmgr`, always this name),
  `Boot/BCD`, `Boot/boot.sdi`, `Boot/boot.wim`,
  `EFI/Microsoft/Boot/Fonts/*.ttf` (from `Fonts_EX` with `_EX` removed when
  serving the 2023 boot manager, else `Fonts`). Nothing at `\BCD`. All files
  come from the built WIM, not the media root.
- **Defaults**: ~~`sourceIndex` 1 (plain WinPE: smaller, and no Setup program
  that blocks in WDS mode).~~ Superseded by the 2026-11-18 amendment below.
  `optionalComponents` and `produceHttpBootIso`
  stay in the spec but are refused with `422 not_implemented`.
- **Caching**: `inputHash` is the SHA-256 of a canonical JSON document of
  every input: a recipe version (bumped when the build code changes), the
  `boot.wim` blob hash, index, architecture, resolved builder, agent binary
  hash, server URLs, `bootmgr`, TFTP sizes, and each enabled INF (package
  hash + path). A build whose hash matches the boot image's current build,
  or another boot image's, returns 200 and reuses that tree without a job.
- **Stale**: the inputs of the last build are stored; a boot image is
  `stale` (with `staleReasons`) when the current inputs differ (the builder
  only counts when chosen explicitly). It is computed when read, so driver
  group and INF changes, edits and a new bundled agent show at once.
- **Serving**: a boot image keeps serving its last good tree while stale,
  building, or after a failed rebuild.
- **Checks**: refuse `unsigned` drivers (Secure Boot), warn on `vendor`;
  refuse INFs that don't cover the image's architecture; refuse a `bootmgr` the source index lacks.
  Images and driver groups used by a boot image can't be deleted (`409`).
- **Where builds run**: in the job's staging directory under
  `<content>/staging`, with free space for about four times the source
  `boot.wim` checked first. Cancelling takes effect between steps; a DISM
  step that is running is allowed to finish and its mount is discarded.
  At startup, before staging is emptied, DISM mounts under our staging
  directory are discarded and `/Cleanup-Mountpoints` runs. That needs
  elevation; unelevated, the server logs an error and leaves them (staging
  cleanup skips what it can't remove instead of refusing to start).

## Consequences
- A Windows host running elevated needs no extra software; a Linux host
  needs `wimtools`, and a non-elevated Windows run (development) needs
  wimlib installed.
- Every driver works on every host; the installed OS's storage driver is
  the agent's job.
- Changing a boot image's inputs never interrupts clients booting it.
- Tested 2026-11-08 (Phase 4 findings): DISM and wimlib builds of the
  Win11 boot.wim both boot `nd-win` under Secure Boot with the 2023 boot
  manager and the `Fonts_EX` set, and the agent starts from `winpeshl.ini`.
  wimlib's customise step took 1.3 s against DISM's 43 s.
- Still to verify: DISM exporting straight from a content-store blob; SMB
  installs (`setup.exe` from a share) running in plain WinPE (index 1).

## Amendment (2026-11-18): the Setup image by default

**Context.** Adding OS drivers in the agent (`dism /Image:W:\ /Add-Driver`,
Phase 4) failed on every lab deployment from index 1 with `0x80004002`
(E_NOINTERFACE): DISM copies itself out of the target image, starts
`dismhost.exe`, and creating its DismHostManager object fails. The same DISM
binaries, the image's own DISM and the media's DISM all fail the same way
in plain WinPE, for Windows 11 and Server 2025; the same command from boot.wim
index 2 (Windows Setup) succeeds with WinPE's own DISM. Index 2 adds the
WinPE-Setup, WinPE-Setup-Client and imagebasedsetup-media packages
(Phase 5 findings). The Setup program that blocked in WDS mode (Phase 1)
never starts here: our `winpeshl.ini` replaces the shell.

**Decision.**

- When `sourceIndex` is omitted, a boot image uses boot.wim's own boot index
  from the WIM header: the Windows Setup image, 2 on install media. It falls
  back to 1 if the header names no listed index. Existing boot images keep
  the index they were saved with.
- Each build records whether the built WinPE has the WinPE-Setup package
  (`Windows/servicing/Packages/WinPE-Setup-Package~*.mum`):
  `BootImage.canAddOsDrivers` (read-only; null until built, and for builds
  made before this). A build without it gets a warning.
- `validate` (and so arming and starting from the boot prompt) reports
  `boot_image_cannot_add_drivers` for a profile with OS driver groups: an
  error when its boot image can't add them, a warning while it's unknown.
  The deployment is refused before a disk is wiped instead of failing at
  `injecting-drivers`.
- The recipe version goes to 2, so every existing build shows `stale`
  (`recipe`) and a rebuild records the capability. Until rebuilt it keeps
  serving, `canAddOsDrivers` is null, and validate reports the stale boot
  image as usual.

**Consequences.** Boot images are about 0.1 GB bigger expanded (2.56 GB
against 2.45 GB); TFTP download time grows a little. Plain WinPE remains
selectable for profiles without OS drivers. Lab: index 2 deploys Windows 11
Pro with 101 Intel chipset drivers added (Phase 5 findings).

