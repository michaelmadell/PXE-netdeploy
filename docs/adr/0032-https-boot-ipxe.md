# 32. HTTP(S) Boot through the signed iPXE shim and wimboot

Status: accepted (2026-11-20); amended 2026-11-23 (`snponly.efi`, see
the end). Phase 6 decision 2. Narrows
[0002](0002-separate-windows-boot-path.md) and replaces the HTTP Boot
answer deferred in [0024](0024-booter.md). Builds on
[0028](0028-content-https.md) and [0031](0031-packaging.md).

## Context
UEFI HTTP Boot clients were never answered: the plan was a RAM-disk ISO
(`produceHttpBootIso`, Phase 1 task 9). The spike (Phase 6 findings, stages
1–4) showed:

- QEMU/OVMF boots such an ISO, but the lab's Intel NUC firmware only runs EFI
  programs over HTTP(S) Boot. It does not boot RAM-disk images, whatever
  Secure Boot is set to.
- `bootmgfw.efi` started over HTTP can't fetch its BCD and boot.wim: it
  only knows PXE/TFTP and local devices.
- Microsoft-signed iPXE shim → iPXE-signed `ipxe.efi` → a script →
  Microsoft-signed `wimboot` → our usual `bootmgfw.efi`, BCD, boot.sdi and
  boot.wim boots WinPE on the NUC with Secure Boot on. Everything goes over
  HTTPS, and it was faster than the ISO. iPXE (since March 2025) trusts the
  CA held in the firmware's `TlsCaCertificate` variable, so enrolling our
  content CA for HTTPS Boot covers iPXE too. Our BCD works unchanged
  under wimboot.

ADR 0002 kept shim and iPXE off the Windows path because some machines
(Secured-core PCs) trust only the Windows CA. That still holds for PXE.
HTTP(S) Boot can't load Windows directly anyway, so on this path the
third-party CA requirement is the price of using it.

## Decision
- **HTTP(S) Boot clients** (DHCP vendor class `HTTPClient`, or a URL typed
  into the firmware) get the iPXE chain. **PXE clients are unchanged**: they
  get `bootmgfw.efi` directly over TFTP (ADR 0002).
- **The chain**, served from the content listeners under
  `/content/boot/ipxe/<arch>/`, where `<arch>` is `x64` or `arm64`:
  `ipxe-shim.efi` (loaded by the firmware), `ipxe.efi` (loaded by the shim
  from the same directory), `autoexec.ipxe` (fetched by iPXE from the same
  directory) and `wimboot`. The script is generated: it loads `wimboot`,
  then `bootmgfw.efi`, `BCD`, `boot.sdi` and `boot.wim` as initrds by
  absolute path (`/content/boot/bootmgfw.efi`, `/content/boot/Boot/BCD`, …),
  so they come over the same scheme, host and port as the script. If
  anything fails it prints why and returns to the firmware after 15 seconds,
  with no iPXE shell.
- **What a client boots is decided as for PXE** (ADR 0024). The boot image
  files under `/content/boot/` come from the client's session, lease or the
  default profile. The `ipxe/` directory is the same for every client and
  takes precedence over the tree (no boot image has an `ipxe/` directory).
  A typed URL works without our DHCP: the client then gets the default
  profile's boot image, as with DHCP `off`.
- **DHCP** (full and proxy modes) answers `HTTPClient` requests when
  `architectures.httpBoot` is on. The answer is
  `https://<interface address>:<httpsPort>/content/boot/ipxe/<arch>/ipxe-shim.efi`
  when the interface serves HTTPS and the content CA exists, else the
  `http://` URL on `httpPort`. If the interface serves neither, or the
  bundle lacks the architecture, there's no answer, and the log says why.
- **Binaries are bundled** in the installer and the `.deb`, next to the
  executable in `ipxe/<arch>/`, with their licences and a source note (GPLv2,
  as with wimlib). `-ipxe-dir` overrides the location (development, or a
  newer set). `scripts/fetch-ipxe.ps1` downloads them with pinned SHA-256s
  for builds and CI:

  | File | From | Signed by |
  |---|---|---|
  | `ipxe-shim.efi` | `ipxe/shim` release `ipxe-16.1` (`ipxe-shimx64.efi`, `ipxe-shimaa64.efi`) | Microsoft UEFI CA 2011 **and** 2023 |
  | `ipxe.efi` | iPXE v2.0.0 `ipxeboot.tar.gz`, `x86_64-sb/snponly.efi` and `arm64-sb/snponly.efi`, renamed (amendment below) | iPXE Secure Boot CA |
  | `wimboot` | wimboot v2.9.0 (`wimboot`, `wimboot.arm64`) | Microsoft UEFI CA 2011 and 2023 |

  The shim inside the iPXE v2.0.0 archive is signed only by the 2011 CA,
  so it isn't used. The archive's full `ipxe.efi` was the one first tested;
  `snponly.efi` replaced it on 2026-11-23 (amendment below). Only
  these precompiled signed builds work under Secure Boot; a custom iPXE
  build would not.
- **The server reads the bundle at start** and reports it in capabilities
  (`httpBoot`: architectures, path, each file's SHA-256). A missing bundle
  with `architectures.httpBoot` on gives the health warning
  `http_boot_unavailable`.
- **Content TLS** always offers `TLS_RSA_WITH_AES_128_GCM_SHA256` and
  `TLS_RSA_WITH_AES_256_GCM_SHA384` along with Go's defaults. OVMF's TLS 1.2
  stack offers only RSA key exchange (spike, stage 2); the NUC (TLS 1.3) and
  iPXE (TLS 1.2 ECDHE) don't need them.
- **The RAM-disk ISO is kept for later.** It could help firmware that
  trusts only the Windows CA and boots RAM disks. `produceHttpBootIso: true`
  is still refused with `not_implemented`.

## Consequences
- HTTP(S) Boot works on firmware that runs EFI programs only, with no
  image building or extra storage. The files are the ones PXE serves, plus
  about 1.5 MB per architecture.
- Machines that HTTP(S)-boot must trust Microsoft's third-party UEFI CA (the
  2023 one is enough with the ipxe-16.1 shim). Windows-CA-only machines
  (some Secured-core PCs) need that option enabled, or need PXE. The UI and
  the lab guide say so.
- The firmware must trust our content CA for HTTPS (`/settings/content-ca`
  as DER, ADR 0028); iPXE then trusts it too. Plain HTTP needs nothing, and
  Secure Boot still verifies every binary.
- iPXE does its own DHCP after it starts. Our DHCP answers it like any PXE
  client, which only refreshes the session; the script ignores the boot file.
- Updating iPXE, the shim or wimboot means a netdeploy release (new pins),
  or `-ipxe-dir` pointing at a newer set.
- The first boot of a shim can update the machine's revocation state
  (SBAT). On the NUC that one boot ended in a firmware "Secure Boot
  Violation" after everything had downloaded, and it never recurred with
  either shim (findings, stage 4). If a machine shows it once, boot again
  before investigating.
- arm64 is bundled but untested (no arm64 lab machine).

## Amendment, 2026-11-23: `snponly.efi` instead of the full `ipxe.efi`

**Context.** In the Phase 7 spike (Phase 7 findings, "Intel NUC") the
NUC's USB keyboard did nothing in an iPXE menu loaded by the full
`ipxe.efi`. That build has its own USB host controller drivers; it takes
the controllers from the firmware and offers `EFI_USB_IO_PROTOCOL` in their
place, which many vendors' keyboard drivers don't attach to. This is a known
iPXE problem (for example [ipxe/ipxe discussion #1182](https://github.com/ipxe/ipxe/discussions/1182));
the fixes people use are custom builds (no USB drivers, or `USB_HCD_USBIO`),
which the iPXE shim refuses under Secure Boot, and iPXE has no runtime
setting for it. Phase 7 puts interactive steps in iPXE (menus, a prompt:
decision 3), so the keyboard has to work.

`snponly.efi`, built and signed alongside `ipxe.efi` in the same release
archive, drives only the network card the machine booted from, through the
firmware's own network driver (SNP), and leaves USB to the firmware.

**Tested on the NUC, Secure Boot on, over HTTPS, 2026-11-23:** the keyboard
works in the iPXE menu; Windows boots through it (WinPE and the netdeploy
agent with network; boot.wim, 599 MB, in 17.5 s, the same as the full build's
17.6 s); Ubuntu 26.04.1 and 24.04.5 boot through iPXE's `shim` command to the
installer. On OVMF with Secure Boot it also loaded and started Ubuntu's
kernel (the Phase 7 B2 rerun). Not tested: arm64.

**Decision.**
- The bundle's `<arch>/ipxe.efi` is the archive's `<arch>-sb/snponly.efi`,
  under the name `ipxe.efi` because the iPXE shim loads that name. Pinned
  SHA-256: x64 `b1e67c3e4a1e8708ddfd0079ad4505e3a02245acb55ee9a95437ab3c507be82a`,
  arm64 `bed8da8639b45eeec55d41627349863efa5085a2113bb1193838b8a463e5f49e`.
- No code, spec or UI change: the file name, paths and capabilities report
  are the same (capabilities show the new hash).
- The full build stays available by pointing `-ipxe-dir` at a copy of the
  tree with it in place of `ipxe.efi`.

**Consequences.**
- iPXE uses the firmware's network driver only. A machine whose firmware
  network driver is poor loses iPXE's own drivers as a fallback; such a
  machine would also HTTP Boot poorly, since the firmware fetched the shim
  with the same driver.
- Only the NIC the machine booted from is used (as before in practice).
- A netdeploy release changes the binary that every HTTP(S) Boot client
  runs: re-test on the NUC with netdeployd's own DHCP answer after
  updating.
