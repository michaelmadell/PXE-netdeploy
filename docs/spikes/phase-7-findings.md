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
| **C**, progress when the session stopped (12:59) | Curtin had partitioned and formatted `vda` and was extracting the system (`stage-extract`, still running after 13 minutes under TCG). Install end, the `late-commands` hook, power-off and the installed system's own Secure Boot boot **not yet seen** |

**Intel NUC, 2026-11-20 (partial):** with the typed URL
`https://10.77.0.1:8444/ipxe-shim.efi`, the NUC fetched the shim and
`ipxe.efi` (firmware, TLS 1.3) and iPXE fetched the generated menu script
(TLS 1.2 ECDHE) at 12:47. No chain was run from it yet.

### Still to do

| Item | How |
|---|---|
| C to the end | Watch for `/report/late` and the VM powering off, then boot it with `-Choice x` (the menu returns to the firmware, which boots the disk) and check the installed system starts under Secure Boot (`/var/log/sb-state.txt` is written by `late-commands`) |
| NUC: B, B2 (ISO over HTTPS), B4 (24.04.5), E (`sanboot`) | Typed URL as above; the menu defaults to B after 30 s. C wipes the largest disk |
| D (iPXE → Ubuntu shim → GRUB) | Exploratory; only needed if B fails on real hardware |
| HTTPS for casper's ISO download and for cloud-init's NoCloud fetch | B2 on the NUC; NoCloud over HTTPS needs our CA trusted inside the installer, which nothing provides yet |
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

- **The boot path for Linux is settled in principle:** the bundled chain plus
  iPXE's `shim` command and the distribution's own shim. No GRUB, no new
  bundled binaries, no custom iPXE build; the per-client iPXE script carries
  the kernel, initrd, distribution shim and command line. To confirm on the
  NUC.
- **Import (decision 2)** must record, per Linux image, the shim
  (`EFI/BOOT/BOOTX64.EFI`) with all its signers, the kernel and initrd paths,
  and serve the ISO itself for casper's `url=`.
- **Autoinstall (decision 4):** NoCloud over our content listener works;
  serving an empty `network-config` would save cloud-init's five 404
  retries. `cloud-config-url=/dev/null` belongs in every generated command
  line that uses `url=`.
- **Status (decision 5):** Subiquity's `reporting` webhook gives start/finish
  events for every install step with no agent. Set its `level` to `INFO` to
  cut the volume; `early-commands`/`late-commands` hooks mark the start and
  end explicitly.
