# Phase 7: findings

Measured behaviour and facts established while building Phase 7,
non-Windows operating systems (from 2026-11-20). Earlier results:
[phase-6-findings.md](phase-6-findings.md).

## Secure Boot Linux through the iPXE chain (decision 1 spike)

### Setup

- Media, downloaded with their published SHA-256s (both matched):
  `ubuntu-26.04.1-live-server-amd64.iso` (2.79 GB, "Ubuntu-Server 26.04.1
  LTS ... (20260826)") and `ubuntu-24.04.5-live-server-amd64.iso`
  ("Ubuntu-Server 24.04.5 LTS ... (20260909)"). Kept on the lab host in
  `C:\netdeploy\spike-linux` (D: is nearly full), not in the repo.
- `httpboot-spike linux` (`.tmp/httpboot-spike/linux.go`, not committed):
  serves a tree over HTTP :8081 and HTTPS :8444 with the stage 1–4 spike CA,
  renders any `name.tmpl` as `name` per request (so the iPXE script and the
  NoCloud files can name the server's own address), and logs `/report/...`
  requests with their bodies.
- The iPXE script is a menu (`.tmp/linux-spike/www-src/autoexec.ipxe.tmpl`)
  with one entry per chain, so one boot of a machine can try any of them.
- `.tmp/linux-spike/sigs` lists **every** Authenticode signature of an EFI
  binary and the Microsoft CA each chains to (the product's classifier stops
  at the first), plus the `.sbat` section.
- `.tmp/linux-spike/qemu-sb.ps1`: Debian's OVMF 2025.02 with Secure Boot
  (`OVMF_CODE_4M.secboot.fd`, `OVMF_VARS_4M.ms.fd`: Microsoft keys
  enrolled), q35 with SMM, HTTP Boot from QEMU's user network. WHPX has no
  SMM ("System Management Mode not supported by this hypervisor"), so these
  runs use TCG.

### iPXE's `shim` command (from the v2.0.0 source)

The bundled `ipxe.efi` (v2.0.0) has a `shim` command
(`src/hci/commands/shim_cmd.c`, `src/interface/efi/efi_shim.c`,
`src/image/efi_image.c`):

- `shim <uri>` downloads a distribution's shim and registers it. When an
  EFI image is booted later, iPXE first checks whether the firmware will
  load it (`efi_can_load`). If it won't (Secure Boot refuses a kernel signed
  by a distribution key), iPXE starts the registered shim instead, with the
  image as shim's second stage, served from iPXE's own
  `EFI_SIMPLE_FILE_SYSTEM_PROTOCOL`. The kernel command line goes through
  as load options.
- Defaults that work around shim: it doesn't insist on a second-stage loader
  calling the shim lock protocol (`--require-loader` restores that), the PXE
  base code is stopped so shim can't refetch over TFTP (`--allow-pxe`), and
  the `SbatLevel` shim reads is patched because shim's SBAT checks would
  refuse current kernels (`--allow-sbat`).
- With no argument, `shim` unregisters.

### Signers

| File | Signed by | SBAT |
|---|---|---|
| 26.04.1 `EFI/boot/bootx64.efi` (shim 15.8-0ubuntu2) | Microsoft UEFI CA **2011 only** | `shim,4`, `shim.ubuntu,1` |
| 26.04.1 `EFI/boot/grubx64.efi` (GRUB 2.14-2ubuntu1) | Canonical Secure Boot Signing (2022 v1) | `grub,5`, `grub.ubuntu,2`, `grub.ubuntu26,1`, `grub.peimage,2` |
| 26.04.1 `EFI/boot/mmx64.efi` | Canonical (2022 v1) | `shim,4` |
| 26.04.1 `casper/vmlinuz` (7.0.0-30-generic) | Canonical (2022 v1) | none (kernels carry no SBAT) |
| 24.04.5 `EFI/boot/bootx64.efi` (shim 15.8-0ubuntu1) | Microsoft UEFI CA 2011 only | `shim,4`, `shim.ubuntu,1` |
| 24.04.5 `EFI/boot/grubx64.efi` (GRUB 2.12-1ubuntu7.3) | Canonical (2022 v1) | `grub,5`, `grub.ubuntu,2`, `grub.peimage,2` |
| 24.04.5 `casper/vmlinuz` | Canonical (2022 v1) | — |

Neither release's media has a 2023-signed shim yet. Canonical says shims
built after the 2011 CA's June 2026 expiry will be signed with the 2023 CA
([Ubuntu Discourse](https://discourse.ubuntu.com/t/microsoft-uefi-ca-rotation-what-it-means-for-ubuntu-users-and-vendors/82652)).
Until then, **Ubuntu needs the 2011 UEFI CA in DB** on any path, ours
included: the dual-signed iPXE shim gets iPXE running on a 2023-only machine,
but Ubuntu's shim would then be refused. Checked by signature, not yet by a
boot.

### OVMF with Secure Boot, 2026-11-20

| Run | Result |
|---|---|
| The iPXE chain itself (shim → `ipxe.efi` → script) under Secure Boot | **Worked** (first OVMF Secure Boot run of the ADR 0032 chain): the firmware's HTTP Boot fetched the dual-signed shim, the shim fetched `ipxe.efi`, iPXE fetched the script |
| **A**: `kernel` + `initrd` with no distribution shim | **Refused**, as expected: `Verification failed: Security Policy Violation` (printed by the iPXE shim's verifier) |
| **B**: `shim u2604/shimx64.efi`, then `kernel`/`initrd` (26.04.1), `ip=dhcp url=http://…/ubuntu.iso cloud-config-url=/dev/null` | **Worked.** iPXE prints the same `Verification failed` line for its `efi_can_load` probe, then starts Ubuntu's shim, which verifies and starts the kernel. Kernel log: `secureboot: Secure boot enabled`, `Kernel is locked down from EFI Secure Boot mode`, our command line intact, the initrd found (iPXE's LoadFile2 initrd). casper downloaded the 2.79 GB ISO over HTTP with wget (80 s under TCG), mounted it, and the live system booted |
| TCG artefact | The first B boot panicked: `IO-APIC + timer doesn't work!`. QEMU/TCG on this host only; `no_timer_check` (added by `qemu-sb.ps1 -Tcg` through the server's `extra` file) avoids it. Not for real hardware |
| **C**: B plus `autoinstall ds=nocloud;s=http://…/nocloud/`, without `cloud-config-url=/dev/null` | cloud-init (local stage) took `url=` as a cloud-config URL and started downloading the 2.79 GB ISO a second time (`ua="Cloud-Init/26.1-0ubuntu3~26.04.1"`, 2.65 GB in 4 minutes before the VM was stopped). **`cloud-config-url=/dev/null` is required whenever `url=` is used** |
| **C** with `cloud-config-url=/dev/null` | cloud-init fetched `network-config` (404, retried 5 times about a second apart), `meta-data`, `user-data`, `vendor-data` from our URL; `;` in `ds=nocloud;s=…` passes through iPXE unchanged. The `early-commands` hook posted the kernel command line. Subiquity's `reporting` webhook posted JSON events (`name`, `description`, `event_type` start/finish, `result`, `level`, `timestamp`; user agent `Curtin/…`), about 150 in the first minute at the default level, most of them `DEBUG`. `autoinstall` on the command line skipped the confirmation (`NEEDS_CONFIRMATION` → `RUNNING` with no input) |
| **C** to the end | **Worked.** The `late-commands` hook reported at 15:01 (about 2.5 hours under TCG, most of it extracting the system and applying security updates), then the VM powered off as `shutdown: poweroff` asked |
| The installed system, 2026-11-23 (`qemu-sb.ps1 -Choice x`: the menu returns to the firmware) | **Booted under Secure Boot** from its disk through Ubuntu's own shim and GRUB: `secureboot: Secure boot enabled`, locked down, kernel 7.0.0-44 (updated during the install from the ISO's 7.0.0-30), hostname `nd-spike`. The firmware still tried **network boot first**: curtin didn't put its `ubuntu` entry ahead of the network in this VM's boot order. The install also copied the console arguments from the installer's command line into the installed GRUB configuration |

**Intel NUC, 2026-11-20:** with the typed URL
`https://10.77.0.1:8444/ipxe-shim.efi`, Secure Boot on:

| Run | Result |
|---|---|
| **B** (the menu's default after 30 s) | The firmware fetched the shim and `ipxe.efi` (TLS 1.3); iPXE fetched the script, Ubuntu's shim, the kernel and the initrd over HTTPS (TLS 1.2 ECDHE; the 99 MB initrd in 2.9 s). The **kernel ran**: it took its own DHCP address (`10.77.0.103`; iPXE had `.102`) and casper downloaded the 2.79 GB ISO over HTTP in **24.8 s**, and the live installer reached its **language selection screen**. **B works on real hardware under Secure Boot** |
| The keyboard in the iPXE menu | **Didn't work** (the user couldn't move off the default), so no other entry could be chosen. Likely cause, not yet confirmed: the full `ipxe.efi` has its own USB host drivers (EHCI, UHCI, xHCI in `config/usb.h`) and takes the controller from the firmware; on EFI it re-offers `EFI_USB_IO_PROTOCOL` so the firmware's keyboard driver can carry on (`USB_EFI`), which evidently didn't work on this NUC. `snponly.efi` (also iPXE-signed, same archive) drives only the network, through the firmware's SNP, and leaves USB alone. Next boot: `snponly.efi` served as `ipxe.efi` (the full one kept as `www\ipxe-full.efi`) |
| The keyboard with **`snponly.efi`**, 2026-11-23 | **Worked**: entries were chosen within seconds of the menu appearing. Downloads through the firmware's network driver were as fast (the 99 MB initrd in 2.9 s, as with `ipxe.efi`) |
| **B4** (24.04.5, `snponly.efi`) | **Worked**: Ubuntu 24.04.5's shim and kernel, then the 4.08 GB ISO over HTTP in 34.7 s, to the installer |
| **B2** (ISO over HTTPS, `snponly.efi`) | **Failed** after the kernel started: casper printed `wget: error getting response: Connection reset by peer`, then `Unable to find a live file system on the network`, and dropped to the initramfs shell. Reproduced on OVMF with casper's output on the serial console (same messages; BusyBox v1.37.0). The server logged **nothing** for it on :8444, not even a TLS handshake error, though it logs one for a bare TCP connect-and-close or a TLS 1.1 client. So casper's `wget` never connected: BusyBox's `wget` hands `https://` to a helper (`ssl_client` or `openssl`), which the Ubuntu initramfs doesn't seem to have, and reports the closed helper pipe as a reset. **The stock casper initrd can't fetch the ISO over HTTPS** |
| **E** (`sanboot` of the ISO) | The firmware booted the ISO's own shim, GRUB and kernel from iPXE's SAN disk (iPXE read the ISO with HTTP range requests), then casper stopped: "unable to find a medium containing a live filesystem". Once Linux runs, nothing provides that disk (no driver for iPXE's HTTP SAN device), and the ISO's GRUB configuration can't be given `url=`. **Not usable for Linux installs** |
| **W**: Windows through `snponly.efi` (the ADR 0032 script: wimboot, then the stage 4 spike's `bootmgfw.efi`, BCD, boot.sdi, boot.wim) | **Worked**, twice: WinPE started and the netdeploy agent came up with network. boot.wim (599 MB) in 17.5 s both times, the same as the full `ipxe.efi` in stage 4 (17.6 s) |
| netdeployd itself with the amended bundle (ADR 0032), 2026-11-23 | **Worked**: the installed service serving `snponly.efi` as `ipxe.efi`, the NUC HTTPS-booted from netdeployd's own DHCP answer (no typed URL) to WinPE and the agent |
| "Verification failed" after `vmlinuz` (B, B2, B4) | Expected, as on OVMF: iPXE's `efi_can_load` probe asks the firmware to load the kernel directly, the iPXE shim's verifier refuses it and prints the message, then iPXE starts Ubuntu's shim, which verifies the kernel |

### Still to do

| Item | How |
|---|---|
| HTTPS inside the installer | casper's ISO fetch can't do HTTPS (above); cloud-init's NoCloud fetch over HTTPS would also need our CA trusted inside the installer. Untested |
| A second family (Debian or Fedora with kickstart) | If time allows |
| 2023-only DB | Ubuntu's shims are 2011-only (above), so expected to fail; confirm on the NUC with the 2011 CA unticked |

### Resuming

On the lab host: the spike server runs from `.tmp\httpboot-spike`
(`.\httpboot-spike.exe linux -log C:\netdeploy\spike-linux\server.log`,
built from `linux.go`; restart it after a reboot). The served tree is
`C:\netdeploy\spike-linux\www`, rebuilt by `.tmp\linux-spike\setup.ps1`
from the extracted ISOs (`extract.ps1`) and `.tmp\linux-spike\www-src`.
`www\default` picks the menu's default entry; `www\extra` adds kernel
arguments (only `qemu-sb.ps1 -Tcg` writes it: delete it before booting real
hardware). `.tmp\linux-spike\reports.ps1` and `last.ps1` summarise the log.
OVMF screenshots: `.tmp\httpboot-spike\shot.ps1 -File <png>` (QMP on
:4444); serial output in `C:\netdeploy\spike-linux\vm\serial.txt`.

### Consequences so far (for decisions 3–6)

- **The boot path for Linux:** the bundled chain plus iPXE's `shim` command
  and the distribution's own shim, proven on OVMF and on the NUC. No GRUB, no
  new bundled binaries, no custom iPXE build; the per-client iPXE script
  carries the kernel, initrd, distribution shim and command line.
- **Import (decision 2)** must record, per Linux image, the shim
  (`EFI/BOOT/BOOTX64.EFI`) with all its signers, the kernel and initrd paths,
  and serve the ISO itself for casper's `url=`.
- **The ISO goes over plain HTTP** even when the content listener serves
  HTTPS: the shim, kernel and initrd come over HTTPS through iPXE (and are
  signature-checked anyway), but casper's ISO download, the squashfs the
  installed system is copied from, is unauthenticated. The same exposure as
  Windows content over HTTP (ADR 0028); a deployment network is assumed. A
  generated `url=` must therefore use the interface's HTTP address, which
  means an interface that serves HTTPS must also serve HTTP for Linux
  clients. Options for later: casper's `fsck.mode`/`md5sum.txt` checks don't
  cover a tampered ISO; an initramfs hook with a TLS client would.
- **`sanboot` is out**, for Linux at least (above).
- **Autoinstall (decision 4):** NoCloud over our content listener works;
  serving an empty `network-config` would save cloud-init's five 404
  retries. `cloud-config-url=/dev/null` belongs in every generated command
  line that uses `url=`.
- **Status (decision 5):** Subiquity's `reporting` webhook gives start/finish
  events for every install step with no agent. Set its `level` to `INFO` to
  cut the volume; `early-commands`/`late-commands` hooks mark the start and
  end explicitly.
- **ADR 0032 now ships `snponly.efi`** (amended 2026-11-23): with it the
  NUC's keyboard works in iPXE, which anything interactive (a menu, a
  password prompt) needs; Windows (WinPE and the agent) and Ubuntu boot
  through it, and downloads are as fast as with `ipxe.efi`. The full build
  can't be fixed from our side: the known fixes are custom builds, which the
  iPXE shim refuses under Secure Boot.
- **Re-imaging (decision 3):** after the install the machine still
  network-boots first, so a Linux client must not be re-installed just
  because it boots our chain again. This is the same problem ADR 0013 solves
  for Windows, and iPXE has no prompt of ours. Options for decision 3: the
  script falls back to `exit` (back to the firmware, then the disk) unless a
  deployment is armed, or curtin/`late-commands` sets the boot order.

## Linux media import (decision 2, ADR 0033)

**Built 2026-11-23.** `internal/media/iso9660` (Rock Ridge, Joliet, plain
names; multi-extent; symlinks reported, not followed); import reads UDF,
else ISO 9660; `detectLinux` (ubuntu, debian, redhat); `LinuxImageDetails`;
every signer on `BootManager` (`signers`, `signedBy`) for Windows images
too; Ubuntu keeps its ISO.

**Automated** (Windows lab host: `go test -count=1 ./...`, `just lint`):

- `iso9660`: fixtures made by xorriso 1.5.6 in WSL
  (`scripts/make-iso9660-fixtures.sh`): Rock Ridge + Joliet (names,
  Unicode, a 204-character name through a `CE` area, `.disk`, the symlink
  as `ModeSymlink`), Joliet only (xorriso cuts the long name to 104
  characters), plain level 1 (`VMLINUZ.;1` read as `VMLINUZ`); `ReaderAt`
  across blocks; in-memory images for a multi-extent file, an extent outside
  the image, a cut-short multi-extent file, interleaving (refused), a
  directory containing itself and two entries naming one directory (both
  `ErrCorrupt`, so a walk can't loop or explode); `fstest.TestFS`.
- **Fuzzing:** 12 million inputs clean (Linux, `-fuzzminimizetime 0`).
  Without that flag the coordinator spends nearly all its time minimising
  the 48 KiB seeds (17 execs a minute); on Windows, multi-worker fuzzing
  is slow as well.
- `signer`: a binary signed under two known CAs and a vendor key gives
  `Signers` in order and three `Subjects`; vendor-only gives `Unknown`, no
  `Signers`, the subject.
- `imports`: the Rock Ridge fixture imports as `ubuntu` 26.04.1 x64,
  deployable, `retainIso` forced on with the ISO blob, kernel and initrd
  paths and hashes, the unsigned-shim warning, the tree readable; the plain
  fixture imports as Linux too; `detectLinux` cases for Ubuntu arm64 (from
  `.disk/info` and from the shim's name), Debian, Rocky (`.treeinfo`), a
  casper directory without an initrd (warning, `other`), and nothing.

**Real media, 2026-11-23** (`NETDEPLOY_TEST_LINUX_ISO`, lab host):

| ISO | Result |
|---|---|
| `ubuntu-26.04.1-live-server-amd64.iso` | `linux`, `ubuntu`, `Ubuntu-Server` `26.04.1`, x64, deployable, ISO kept; shim `EFI/boot/bootx64.efi` `signers: [uefi-ca-2011]` (Microsoft Windows UEFI Driver Publisher), GRUB and kernel `signedBy` Canonical (2022 v1), `signers: []`; the "2011 CA only" warning. 21.6 s |
| `ubuntu-24.04.5-live-server-amd64.iso` | The same, `24.04.5`. 26.0 s |

The signers match what the spike's `sigs` tool found.

**Through the lab server's UI (the user, 2026-11-23):** Ubuntu 26.04.1
imported as Linux, ready, with its warning. **Debian Live 13.8.0 (xfce) and
the RHEL 10.3 boot ISO imported as Other**: the rules missed them.

| ISO | Why it was missed | Fixed by |
|---|---|---|
| `debian-live-13.8.0-amd64-xfce.iso` | debian-installer is in `install/` (`vmlinuz`, `initrd.gz`), not `install.amd/`; the live system is in `live/`; `.disk/info` ("Official Debian GNU/Linux Live 13.8.0 xfce 2026-11-21T09:15:44Z") names no architecture | `install/` accepted when `.disk/info` names Debian; architecture from the shim's name |
| `rhel-10.3-x86_64-boot.iso` | No `.treeinfo` on boot ISOs; `.discinfo` (`1793810521.107004`, `10.3`, `x86_64`), `images/pxeboot/{vmlinuz,initrd.img}`, `images/install.img` (stage 2) | `.discinfo` with `images/pxeboot/`; the name from GRUB's "Install Red Hat Enterprise Linux 10.3" |

Re-imported with the fix (`NETDEPLOY_TEST_LINUX_ISO`):

| ISO | Result |
|---|---|
| Debian Live 13.8.0 | `linux`, `debian`, "Debian GNU/Linux Live" `13.8.0`, x64, not deployable (reason given), no warnings; shim `signers: [uefi-ca-2011, uefi-ca-2023]`; GRUB "Debian Secure Boot Signer 2022 - grub2", kernel (`install/vmlinuz`) "Debian Secure Boot Signer 2022 - linux". 33 s |
| RHEL 10.3 boot ISO | `linux`, `redhat`, "Red Hat Enterprise Linux" `10.3`, x64, not deployable, no warnings; shim `signers: [uefi-ca-2011, uefi-ca-2023]`, also signed by "Red Hat UEFI Publisher 2024"; GRUB "Red Hat Secure Boot Signing 802", kernel "… 801". 9.5 s |

**Debian's and Red Hat's shims are dual-signed** (2011 and 2023 CAs), so
they'd boot on 2023-only machines; Ubuntu's (shim 15.8) aren't yet.

**Not yet:** the web UI shows Linux images without their details (family,
version, signers); Windows media through the refactored import only by the
UDF fixtures (`NETDEPLOY_TEST_ISO` not re-run).

## What a Linux client boots (decision 3, ADR 0034)

**Built 2026-11-23** (not yet lab-tested). Profiles have a `kind`
(migration 0015 rebuilds `profiles` with per-kind checks); HTTP(S) Boot
clients with a Linux profile get a generated `autoexec.ipxe` (the gate);
`/agent/ipxe/unlock` answers with a menu of Linux profiles,
`/agent/ipxe/start` with the boot commands; `/content/linux/<imageId>/`
serves the image's tree and `image.iso`. Import also warns about a
2023-only shim.

**Automated** (Windows lab host: `go test -count=1 ./...`, `just lint`):

- `internal/ipxe`: the gate (armed, unarmed, debug mode, no password), the
  menu, the boot commands, failures; `Text` refuses `${` and control
  characters.
- `internal/boot`: PXE clients with a Linux profile get no answer (with the
  reason), HTTP Boot clients the iPXE shim; an armed Linux deployment sets
  `Armed`; `LinuxContent` serves the tree and the ISO, not Windows images.
- `internal/content`: the generated script when `Script` says so, the
  static one otherwise; `/content/linux/` with ranges and path refusals.
- `internal/settings`: the script from the session (armed), the default
  profile with no session, the static script for a Windows session.
- `internal/server`: wrong password goes back to the gate; the menu lists
  only Linux profiles for the machine's architecture; tokens are single-use
  and bound to the MAC; the boot commands
  (`shim …/EFI/boot/bootx64.efi`, `kernel … ip=dhcp
  url=http://10.77.0.1:8080/content/linux/<id>/image.iso
  cloud-config-url=/dev/null <kernelArgs>`, `initrd …`); armed start
  consumes the deployment once; WinPE neither lists, starts nor consumes
  Linux deployments; profile API per-kind codes and `kind_changed`.

**Lab, Intel NUC, 2026-11-23 (the user):** the checks in
`running-the-lab.md` ("Linux deployments") **passed**: the gate's countdown
with no key falling through to the disk (`exit 1`), the password prompt
(`login`) with a wrong and a right password, the menu of Linux profiles
(`menu`, `choose`) and the form POSTs (`params`) in the signed
`snponly.efi`, the Ubuntu installer started from the menu and from an armed
deployment through netdeployd (the deployment `pending` → `running`, not
armed on the next boot), and no answer for PXE. The profile editor's kind
choice and Linux fields were used to set it up.


## Automated installs (decision 4, ADR 0035)

**Built 2026-11-23** (not yet lab-tested). `internal/autoinstall`
(templates with YAML escaping, data, the built-in template, checks, SHA-512
crypt); templates' `format` and `builtin-autoinstall`; Linux profiles'
`linux` settings (migration 0016 rebuilds `unattend_templates` and
`profiles`); arming snapshots the template, variables, hostname and
password secret; the boot commands add `autoinstall
ds=nocloud;s=http://<interface>:<port>/content/seed/<token>/`; the content
listeners serve the seed.

**Automated** (Windows lab host: `go test -count=1 ./...`, `just lint`):

- `autoinstall`: SHA-512 crypt matches the specification's vector and an
  OpenSSL 3 one (`openssl passwd -6`); YAML escaping (quotes, `#`, `: `,
  lists, booleans, `raw`, `shquote`) parses back to the input; the built-in
  template checks clean and renders, through yaml.v3, the identity, SSH,
  storage, packages, the `efibootmgr` late command for `disk-first`, a
  locked password with passwordless sudo when there's no password, no SSH
  server without keys; each check; hostname patterns (placeholder serial,
  lowercasing, invalid names).
- `server`: template API (`not_for_format`, `identity_modes_required`,
  `template_invalid`, `template_render`, `format_changed`, the masked
  sample preview); profile codes (`linux_account_required`,
  `reserved_account_name`, `invalid_ssh_key`, `invalid_hostname_pattern`,
  `template_format_mismatch`); `validate` ok; from the iPXE menu the boot
  commands carry the seed URL, `user-data` has the account, keys, the
  hostname from the pattern and a hash that verifies against the secret,
  `meta-data` has the instance id, `network-config` is 404, and the seed is
  gone once the deployment ends.

**Not yet tested:** an install from the seed on the NUC through netdeployd
(the spike's seed was hand-written), the built-in template's `late-commands`
(boot order, sudoers), the locked-password account; the UI (template format,
the profile's install settings) only through `tsc`.

**Armed deployment ignored on the first boot (NUC, 2026-11-23).** The user
saw the unarmed 30 s countdown first, and the armed 10 s one only on the
next boot. The log: a second DHCP server at `10.77.0.2` (the Phase 6
`nd-linux` VM's netdeployd, still running) answered too; the NUC took its
address (`10.77.0.103`, `client selected server 10.77.0.2`) but fetched our
boot files, so netdeployd had no boot session for `.103` and generated the
script from the default profile, unarmed. 29 such requests in the log
since 2026-11-20. Fixes: stop the other server; and full mode now notes the
address a booting client takes from another server as a boot session, as
proxy mode does (`another DHCP server answered a network-boot client` in
the log).

## Linux deployment status (decision 5, ADR 0036)

**Built 2026-11-23** (not yet lab-tested). `/content/report/<token>/`
(`events`, `started`, `finished`, `failed`); `.Report` in template data and
the built-in template's webhook and hooks; milestones from the spike's
webhook log move the stages; interactive installs end `succeeded` when the
boot commands are served.

**Automated:** the built-in template renders the report URLs and its checks
warn without them; from the menu, `user-data` moves the deployment to
`checked-in` 5%, `started` and three webhook events (out of order) to
`finalizing` 90% with the step's message; only `started` and the INFO event
reach the log; a bad event is `400`, an unknown kind `404`; `finished`
marks `succeeded`, after which the seed and reports are `404`; a failed
side step leaves it running, and both a failed install event and
`error-commands` fail it; the content listener routes only `POST` with a
token and a kind.

**Not yet tested:** a real install's events on the NUC (step names,
stages, timings, the inactivity timestamp), the curl hooks in the installer
environment through netdeployd, a failed install.

**Lab, Intel NUC, 2026-11-23 (the user):** an automatic Ubuntu install
through the installed netdeployd completed, with its status and success
shown on the deployment's page. Not yet recorded in detail: the stage
timings, the account and boot order checks in `running-the-lab.md`, and
the failure variant. The page still described a Windows deployment (the
stage names, "Windows was applied … Setup", "Computer name", the cancel
dialog); the UI now words these for Linux deployments.
