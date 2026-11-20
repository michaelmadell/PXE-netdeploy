# Phase 7 handover

Written 2026-11-20 at the end of Phase 6. It gives a new session what it
needs to design and build Phase 7, **non-Windows operating systems**,
without the Phase 6 chat.

## Starting a session

Paste this as the first message:

> We're starting Phase 7 of netdeploy: non-Windows operating systems. Read
> `PROJECT_BRIEF.md` and `phase-7-handover.md` in the project first, then
> `phase-6-findings.md` (the HTTPS boot spike sections), `adr-index.md`
> (especially 0002, 0013, 0018, 0024, 0027 and 0032), `architecture.md` and
> the relevant parts of `openapi.yaml`. The repo is my connected folder
> `PXE-Netdeploy`. Follow "How we work" in the brief: propose the design for
> one area at a time, get my agreement, then change the spec, then
> implement. Start with the spike in decision 1.

## Read in this order

| Doc | Why |
|---|---|
| `docs/PROJECT_BRIEF.md` | Context, decisions, how we work (no co-author trailers in commits; give `git add` lines with commit commands) |
| This file | Phase 7 starting point |
| `docs/spikes/phase-6-findings.md`, "HTTPS boot spike" and "HTTPS boot, built" | How the signed iPXE chain behaves on real firmware; signatures of every bundled binary |
| `docs/adr/0032-https-boot-ipxe.md` | The iPXE chain Phase 7 builds on |
| `docs/adr/0002-separate-windows-boot-path.md` | Windows PXE stays shim-free; Linux is where shim/iPXE belong |
| `docs/adr/0018-reading-media.md` | UDF only; plain ISO9660 refused "until the Linux phase" |
| `docs/adr/0013-boot-prompt-password-gate.md`, `0024-booter.md` | What a client boots and the re-image gate; both assume WinPE |
| `docs/adr/0027-unattend-templates.md` | The template model an autoinstall/kickstart equivalent should follow |
| `docs/dev/running-the-lab.md`, "HTTP(S) Boot" | Running the chain in the lab |
| `api/openapi.yaml` | `ImageKind` (`windows`, `linux`, `other` already exist), `InstallMethod`, profiles, `importImage` (`unsupported_media`) |

## State at handover

Phases 1–6 are built and lab-tested. Phase 6 added:

| Package / file | What it does |
|---|---|
| `internal/contentca` | Content HTTPS: an internal CA (ADR 0028) |
| `internal/server/auth*.go`, `internal/store/users.go` | Accounts, sessions, API tokens (ADR 0029) |
| `internal/hostcheck`, `internal/portowner` | Linux privileges, data-dir checks, port owners (ADR 0030) |
| `cmd/netdeployd/service_windows.go`, `admin.go`; `packaging/`; `scripts/package.ps1`, `version.ps1` | Windows service, Inno Setup installer, `.deb` (ADR 0031) |
| `internal/ipxe` | Loads the signed chain (`<dir>/<arch>/{ipxe-shim.efi, ipxe.efi, wimboot}`), generates `autoexec.ipxe` (ADR 0032) |
| `internal/content` (`Shared`), `internal/boot` (`ImageArch`, HTTP answers), `internal/dhcp` (`HTTPBootBase`) | HTTP(S) Boot clients get `/content/boot/ipxe/<arch>/ipxe-shim.efi`, then the same boot image as PXE |
| `scripts/fetch-ipxe.ps1` | Downloads the chain with pinned SHA-256s into `dist/ipxe` (`just fetch-ipxe`) |

The iPXE script today is fixed: wimboot plus the client's Windows boot
image. Nothing yet decides per client what iPXE should run.

Still refused or not implemented: plain ISO9660 media (`unsupported_media`),
`/smb-targets` and `installMethod: smb`, identity `entra`,
`optionalComponents` and `produceHttpBootIso` on boot images.

## Things Phase 6 established that Phase 7 must respect

| Fact | Consequence for Phase 7 |
|---|---|
| Under Secure Boot only the **precompiled signed** iPXE (`ipxe.efi`, `snponly.efi`) loads through the iPXE shim; a custom build won't | No custom iPXE builds or embedded scripts; everything goes through the generated `autoexec.ipxe` and files served over HTTP(S) |
| The iPXE shim trusts iPXE's own vendor key, **not** distribution keys | A distro kernel loaded directly by iPXE probably won't verify. Decision 1 must find out how to start a signed distro kernel: iPXE's `shim` command (newer iPXE), chaining the distro's own shim + GRUB, or something else |
| The bundled shim is dual-signed (Microsoft UEFI CA 2011 and 2023); the archive's shim is 2011-only | Distro shims must be checked the same way (Authenticode signers, as ADR 0018 does for boot managers) |
| iPXE trusts the firmware's `TlsCaCertificate` (the enrolled content CA) | HTTPS for kernels, initrds and installer content needs no extra trust work once HTTPS Boot works |
| iPXE does its own DHCP after it starts, and the server answers it like a PXE client | Fine today; a Linux flow that relies on iPXE's DHCP must not get a Windows boot file back. Consider recognising iPXE's user class (option 77 `iPXE`) |
| PXE clients get `bootmgfw.efi` directly (ADR 0002) | Linux on **PXE** needs a new path: the shim (and then iPXE or GRUB) over TFTP. Decide whether PXE Linux clients go TFTP → shim → iPXE → HTTP(S) |
| The first boot of a shim may update the machine's SBAT state; one violation was seen once and never again | Expect it when introducing distro shims; boot twice before investigating |
| Validation is the single source of "can this deploy" | Linux profiles need their own checks in `validateProfile` |
| The NUC's firmware runs EFI programs only over HTTP(S) Boot; OVMF also boots RAM-disk images | Don't rely on firmware RAM disks for Linux ISOs either; iPXE `sanboot` of an ISO is an option to test |

## Decisions to make

### 1. Spike: Secure Boot Linux through the iPXE chain (do first)

On the NUC (Secure Boot on) and OVMF, find a chain that boots a
distribution's installer:

- **Ubuntu 24.04 live server:** the `casper/vmlinuz` and `initrd` from the
  ISO, with `ip=dhcp url=…iso` or `autoinstall ds=nocloud;s=http://…/`.
- Ways to start it: iPXE `kernel`/`initrd` directly (expected to fail
  verification); iPXE's `shim` command, if the bundled v2.0.0 has it;
  chaining Ubuntu's own signed `shimx64.efi` → `grubx64.efi` with a
  generated `grub.cfg` over HTTP; `sanboot` of the ISO.
- Record the signers of Ubuntu's shim, GRUB and kernel (2011 or 2023 CA?).
- A second family (Debian or RHEL-like, with kickstart) if time allows.

### 2. Media: ISO9660 import (ADR 0018)

ISO9660 with Rock Ridge and Joliet, El Torito, and detecting the
distribution (`.disk/info`, `casper/`, `.treeinfo`, `install.amd/`, …).
Record the kernel and initrd paths, the version, the architecture, and the
signers of any `EFI/BOOT/BOOTX64.EFI` and GRUB. `ImageKind: linux` already
exists in the spec; `details` needs a Linux shape.

### 3. What a Linux client boots (ADR 0024, 0013)

Profiles that name a Linux image instead of a boot image; the per-client
iPXE script (or `grub.cfg`) generated from the decision, as Windows clients
get their boot image tree today. Whether the boot-prompt password gate
(ADR 0013) applies, and how: an iPXE menu with a password, a GRUB password,
or only armed deployments. iPXE menus for choosing between profiles were
an early roadmap idea (`architecture.md`).

### 4. Automated installs (ADR 0027 equivalent)

Ubuntu autoinstall (cloud-init NoCloud: `user-data`, `meta-data` served per
deployment), later kickstart and preseed. Templates as for unattend:
whole-document Go templates, secrets never in previews, a built-in template
per family. Accounts, SSH keys, hostname patterns, the disk layout.

### 5. Deployment status without the WinPE agent

The Windows agent reports progress; a distribution installer won't. Options:
installer hooks (`late-commands`, `%post`) calling a small `/agent/*`
endpoint with a per-deployment token; a first-boot check-in; or only
"started" and "served the last file". Timeouts (ADR 0026) depend on this.

### 6. PXE (TFTP) path for Linux

Whether PXE Linux clients get the shim over TFTP and continue over HTTP(S),
and how that sits with ADR 0002 (Windows PXE stays direct).

## Carried over

| Item | Status |
|---|---|
| HTTP(S) Boot: 2023-only DB (2011 CA unticked), proxy-mode answer, OVMF against netdeployd, arm64 | Not yet run (Phase 6 findings) |
| Proxy DHCP :4011 boot server path, `listenOn67: false` | Untested with real firmware |
| The service's recovery actions; the release workflow (`v*` tag) | Not yet run |
| Windows `ownerProcess` on a real conflict | Not yet run |
| Content HTTPS: clock set back, `agent_over_http`, `secrets_key_not_exported` in the lab | Not yet run |
| `just dev` over the LAN; lab scripts with a token | Not yet run |
| Prompt timeout's `BootNext` preferring an existing partition | Unit-tested; not watched in the lab |
| `cloudsoda/go-smb2` | Pinned by commit; NTLM tested, Kerberos not |
| Raw Go error text in problem `detail` fields | Open |
| Code signing for releases | Unsigned until an open-source code-signing certificate (ADR 0031) |
| Deferred, unscheduled | Entra / Autopilot (needs an Intune tenant); SMB / `setup.exe` install (ADR 0007); the HTTP Boot RAM-disk ISO (ADR 0032) |
| UI | HTTP Boot has no UI beyond the `architectures.httpBoot` setting and health warnings; `capabilities.httpBoot` isn't shown anywhere |

## Lab and environment

- Lab host: repo at `D:\PXE-Netdeploy`, server data at `C:\netdeploy\data`
  (`just dev -DataDir C:\netdeploy\data`). Go 1.27.3, PowerShell 7, `just`,
  pnpm, wimlib in `.tmp\wimlib`, Inno Setup, QEMU 10 with Debian's OVMF
  (`.tmp\httpboot-spike\out\ovmf-code.fd`).
- Hyper-V: `NetDeployLab`, host `10.77.0.1/24`; `nd-client-01`; `nd-linux`
  (Ubuntu, the `.deb` installed); the AD test domain at `10.77.0.10`.
- **Intel NUC** (ASUS, i5-1335U, I210 + I219) on the lab network, typically
  `10.77.0.102`: HTTPS Boot only (no plain HTTP), runs EFI programs only,
  TLS 1.3. DB: Microsoft UEFI CA 2011 and 2023, Windows UEFI CA 2023,
  Windows Production PCA 2011, Option ROM UEFI CA 2023, an ASUS key. It
  trusts the lab server's content CA.
- The HTTPS boot spike (`.tmp/httpboot-spike`, not committed): a standalone
  HTTPS server that logs every request and ClientHello (`serve -dir out`),
  and QEMU scripts (`qemu.ps1 -Https -Tcg`). It's handy for trying chains
  before netdeploy serves them.
- The desktop shell Claude uses runs unelevated; installers, firewall and
  Hyper-V steps are run elevated by the user.

## Gotchas met in Phase 6

- EDK II's network stack (OVMF) needs an RNG device since the PixieFAIL
  fixes: add `-device virtio-rng-pci`, or there's no network boot at all.
- OVMF's HTTPS Boot speaks TLS 1.2 with RSA or DHE key exchange only; Go
  needs the RSA-GCM suites added (done on content listeners).
- Firmware `TlsCaCertificate` for OVMF: `-fw_cfg name=etc/edk2/https/cacerts`
  with an `EFI_SIGNATURE_LIST`.
- nfpm `type: tree` gives directories the files' mode (0644, untraversable):
  use globs per directory.
- PowerShell `Set-Content` writes CRLF; Go files must stay LF (gofmt
  complains). Edit with the Linux shell or `sed`.
- Long `Start-Sleep` in desktop-commander processes hangs the output; use
  `read_process_output` with a timeout.
- The cloud sandbox can download GitHub release assets but not use
  `api.github.com`; the lab host can.
- `project_write` with `local_path` needs the staged file read in full in
  the same chat, after its last change.
