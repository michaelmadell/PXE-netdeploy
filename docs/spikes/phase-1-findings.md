# Phase 1 spike: findings

Status: **in progress**. Plan: [phase-1-spike.md](../plan/phase-1-spike.md).
How each task is run: [phase-1-runbook.md](phase-1-runbook.md).

Mark every result **tested** (with the date and setup) or **untested**.

## Setup used

| Item | Value |
|---|---|
| Host OS / build | |
| Windows 11 media (version, build, ISO name) | |
| boot.wim index booted | |
| netdeployd commit | |
| Hyper-V version | |
| I210 device (make, model, firmware version) | |

## Already established (before the lab runs)

| Finding | Evidence |
|---|---|
| `pin/tftp/v3` has no RFC 7440 `windowsize`; we wrote our own TFTP server | Library source; [ADR 0012](../adr/0012-own-tftp-server.md) |
| `insomniacslk/dhcp` on Windows binds a plain UDP socket to the given address (no device binding); on Linux it uses `SO_BINDTODEVICE` | `server4/conn_windows.go`, `conn_unix.go` |
| TFTP windowing, rollover past block 65535, lost-ACK recovery and tsize probes work against our test client | `go test ./internal/tftp` (loopback) |
| **Tested 2026-11-05.** The media's `efi\boot\bootx64.efi` is signed by Windows Production PCA 2011; `Windows\Boot\EFI_EX\bootmgfw_EX.efi` inside `boot.wim` is signed by Windows UEFI CA 2023 (different file, hashes below) | Embedded signer via `X509Certificate.CreateFromSignedFile` |
| `Get-AuthenticodeSignature` reports a **catalog** signature (2011, "Microsoft Windows") for both boot managers, hiding the embedded 2023 signature. Tooling that checks boot manager signers must read the embedded signature, as UEFI does | `SignatureType = Catalog` for both files |
| **Tested 2026-11-05.** Windows 11 media `boot.wim` (Setup, index 2) booted over PXE shows "Windows Deployment Services client functionality is being partly deprecated" and restarts on OK. Microsoft blocks Setup in WDS mode for Windows 11; custom boot images and Setup from a network share remain supported. Confirms the agent / custom-WinPE design (ADR 0007): never rely on media Setup in WDS mode | [WDS boot.wim support](https://learn.microsoft.com/en-us/windows/deployment/wds-boot-support) |
| Since April 2026 WDS also disables hands-free deployment by default (CVE-2026-0386 hardening). Not WDS, so not directly affecting us, but a reason WDS users will look for alternatives | Same page |
| The Hyper-V Default Switch's DHCP (Internet Connection Sharing) holds UDP 67 on its own address (`172.31.224.1`), not `0.0.0.0`; it should not block netdeployd on the lab adapter (confirm in task 1) | `Get-NetUDPEndpoint -LocalPort 67` |

## Task results

| # | Task | Result | Notes |
|---|---|---|---|
| 1 | DHCP, full mode | **Pass** (tested 2026-11-05) | Socket bound to `10.77.0.1:67` receives broadcasts on Windows. WinPE's own client (`MSFT 5.0`, no option 93) got the same address as the PXE stage in ~3 ms |
| 2 | TFTP | **Pass** (tested 2026-11-05) | windowsize negotiated with Boot Manager; block numbers rolled over past 65535 (71,009 blocks) with 0 retransmits. One server bug found and fixed (boot.sdi, below) |
| 3 | WinPE on Windows-CA VM | **Pass** (tested 2026-11-05) | Setup screen reached under Secure Boot from our DHCP + TFTP, then the WDS deprecation block (above). Remaining tasks use index 1 (plain WinPE) |
| 4 | WinPE on third-party-CA VM | **Refused, expected** (tested 2026-11-05) | Secure Boot violation. The `MicrosoftUEFICertificateAuthority` template trusts only Microsoft UEFI CA 2011 and removes Windows Production PCA 2011, so no Windows boot manager can boot. Our docs had this wrong ("+ third-party CA"); fixed. The both-CAs configuration of real PCs needs physical hardware (task 7) |
| 5 | 2023-signed boot manager | **nd-win: boots** (tested 2026-11-05); nd-shim: expected refused (only the third-party CA) | `bootmgfw_EX.efi` (Windows UEFI CA 2023) booted to the WinPE command prompt under Secure Boot |
| 6 | Transfer tuning | **Hyper-V done** (tested 2026-11-05) | 1456/1: 28.5 s; 1456/8: 8.2 s; 8192/8: 2.4 s; 8192/16: 2.1 s; 16384/16: fails. Defaults decided after I210 runs |
| 7 | I210, stock WinPE | | |
| 8 | I210, driver injected | | |
| 9 | HTTP Boot ISO (optional) | | |
| 10 | Linux host | **Pass** (tested 2026-11-05) | Ubuntu VM on the lab switch (`10.77.0.2`) served nd-win: same Secure Boot boot to WinPE as from Windows |

## TFTP requests seen

Full sequence from a Secure Boot boot of nd-win with `bootmgfw_EX.efi` as the
boot file, BCD ramdisk options 16384/16, server caps 8192/8 (tested
2026-11-05). "Probe" means a `tsize` request the client aborts after the OACK.

| # | Client | Requested path | Options | Result |
|---|---|---|---|---|
| 1 | Hyper-V firmware PXE | `bootmgfw_EX.efi` | `tsize blksize=1482 windowsize=4` | Probe (error 8 "User aborted") |
| 2 | Hyper-V firmware PXE | `bootmgfw_EX.efi` | `blksize=1482 windowsize=4` | Served, 3.1 MB in 86 ms |
| 3 | Boot Manager | `\BCD` | `tsize` | Not found |
| 4 | Boot Manager | `\Boot\BCD` | `tsize`, then `tsize blksize=1456` | Probe, then served |
| 5 | Boot Manager | `\EFI\Microsoft\Boot\Policies\SbcpFlightToken.p7b`, `\EFI\Microsoft\Boot\SecureBootPolicy.p7b` | `tsize` | Not found (optional) |
| 6 | Boot Manager | `\EFI\Microsoft\Boot\{Si,SkuSi,WinSi,ATPSi,VbsSi}Policy.p7b`, each up to 3 times | `tsize` | Not found (optional code-integrity policies) |
| 7 | Boot Manager | `\bootmgfw_EX.efi` (its own image, leading `\`) | `tsize`, then `tsize blksize=1456` | Probe, then served at 1456/1 |
| 8 | Boot Manager | `\EFI\Microsoft\Boot\boot.stl`, then `\boot.stl` | `tsize` | Not found (certificate trust list, optional) |
| 9 | Boot Manager | `\Boot\boot.wim`, `\Boot\boot.sdi` | `tsize` | Probes |
| 10 | Boot Manager | `\EFI\Microsoft\Boot\Fonts\wgl4_boot.ttf` | `tsize`, then `tsize blksize=16384 windowsize=16` | Probe, then served (50 KB). The only font requested. First run: not found, as the runbook had copied fonts to `\Boot\Fonts` (fixed) |
| 11 | Boot Manager | `\Boot\boot.sdi` and `\Boot\boot.wim` **in parallel** | `tsize blksize=16384 windowsize=16` (from BCD) | Served at 8192/8; boot.sdi ends `end=tsize` |
| 12 | Boot Manager | `\EFI\Microsoft\Boot\Policies\UnlockToken.pol` | `tsize` | Not found (optional) |
| 13 | WinPE (`MSFT 5.0`) | DHCP only | | Lease `10.77.0.100` |

Observations:

- **BCD path:** Boot Manager tries `\BCD`, then `\Boot\BCD`. Serving a
  per-client BCD (ADR 0006) means answering `\Boot\BCD`, and never having a
  stray file at `\BCD`.
- Boot Manager's own requests use 1456/1 until the BCD is loaded; the font
  and the RAM-disk files use the BCD's `ramdisktftp*` values.
- The firmware's PXE client negotiates `windowsize` too (4 on Hyper-V).
- Boot Manager reuses a source port across requests (9054 for both
  `SbcpFlightToken.p7b` and, 3 s later, `UnlockToken.pol`). Each RRQ is a
  new transfer, so this is fine with per-transfer sockets.
- About 20 "not found" warnings per boot are expected optional files. Phase 2:
  log known optional files at debug level so real misses stand out.

**Boot Manager doesn't ACK an empty final block.** boot.sdi is 3,170,304
bytes = 387 × 8192, so RFC 1350 ends the transfer with an empty block 388.
Boot Manager knows the size from `tsize`, stops after block 387 and never ACKs
block 388. Boot carried on, but the server retried for 6 s and logged
`transfer failed`. Fixed: with `tsize` negotiated and every data byte
acknowledged, a missing ACK for the empty block (timeout or closed port) now
counts as complete, logged with `end=tsize`. Test:
`TestClientStopsAtTsizeWithoutEmptyBlock`.

## BCD and file layout

Differences from the BCD recipe in the plan, and the final layout.

## Secure Boot results

| Boot manager | Signer | nd-win (Windows CA) | nd-shim (+ third-party CA) | I210 device |
|---|---|---|---|---|
| `bootx64.efi` from media (SHA-256 `4BB32C30…B2EB109092`) | Windows Production PCA 2011 | Boots | Refused (Windows CA not in db) | |
| `bootmgfw_EX.efi` from boot.wim (SHA-256 `25D8869E…B72761D1F2`) | Windows UEFI CA 2023 | Boots | Expected refused, not run | |

## Transfer timings (boot.wim, size: 581.7 MB)

Plain WinPE (index 1, exported). Hyper-V internal switch (host to VM, in
memory), so absolute numbers are optimistic; real hardware (task 7) decides
the defaults. BCD requested 16384/16; the server capped it.

| blksize / windowsize | Duration | MB/s | Retransmits |
|---|---|---|---|
| 1456 / 1 | 28.53 s | 20.4 | 0 |
| 1456 / 8 | 8.24 s | 70.6 | 0 |
| 8192 / 8 | 2.44 s, 2.40 s, 2.40 s | 238.6, 242.0, 242.7 | 0, 0, 0 |
| 8192 / 16 | 2.08 s | 279.5 | 0 |
| 16384 / 16 | **Failed** after 77.2 s at block 2881 (47 MB) | – | 76 |

**16384/16 fails.** Each 16 KB block is fragmented into 12 IP fragments, and a
window of 16 sends ~190 fragments back to back; losing any fragment loses the
block. Retransmits came at ~1 s intervals (the server's timeout), so Boot
Manager didn't ACK early to request resends, then stopped ACKing entirely
without sending an ERROR; the VM showed Windows Boot Manager's "Windows failed
to start" error. 8192/8 (~50 fragments per window) has never lost a
packet in four runs. Consequences:

- The server's caps must protect clients from a BCD that asks for too much.
  **Done:** default caps lowered from 65464/64 to **8192/8** (provisional;
  test `TestDefaultCapsLowerLargeRequests`).
- Physical networks fragment worse than Hyper-V's in-memory switch, so the
  I210 runs compare unfragmented 1456 with larger windows against 8192/8.
- Phase 2: shrink the window after repeated loss instead of resending the same
  full window.

Chosen defaults (decided 2026-11-05, Phase 2 decision 2; **provisional**
until the I210 runs in tasks 7–8):

| Setting | Default | Why |
|---|---|---|
| `Settings.tftp.maxBlockSize` | 8192 | 16384/16 failed; 8192/8 never lost a packet (4 runs) |
| `Settings.tftp.maxWindowSize` | 8 | 8192/16 only ~15% faster than 8192/8 and doubles the burst |
| `Settings.tftp.timeoutSeconds` | 1 | Boot Manager never ACKs early, so each loss costs a full timeout (the failed run: 76 retransmits in 77 s at 1 s; >6 min at 5 s). 5 retries drop a dead transfer after ~5 s |
| `BootImage.tftp.blockSize` / `windowSize` | 16384 / 16 | The BCD only *requests*; the server caps decide, and change without a rebuild. A BCD asking 16384/16 capped to 8192/8 boots (tested) |

## I210

## HTTP Boot

## Linux host

Tested 2026-11-05: netdeployd built with `GOOS=linux`, running in an Ubuntu
Server VM (Hyper-V Gen 2, two NICs: Default Switch, and `NetDeployLab` as
`eth1` with static `10.77.0.2/24`), serving nd-win (`MicrosoftWindows`
template, `bootmgfw_EX.efi`).

- DHCP: `bind=0.0.0.0:67` on `eth1` (`SO_BINDTODEVICE`) receives the PXE and
  WinPE broadcasts; offers carry `siaddr=10.77.0.2`. Both socket strategies
  work: interface IP on Windows, wildcard plus device binding on Linux.
- TFTP: identical request sequence to the Windows host, including `\BCD` then
  `\Boot\BCD`, the optional policy files, the font, parallel boot.sdi and
  boot.wim, and `end=tsize` on boot.sdi.
- boot.wim at 8192/8: 2.67 s, 217.5 MB/s, 0 retransmits (VM to VM through the
  vSwitch, versus 2.40 s host to VM).
- Hyper-V DHCP Guard was off (default); with it on, the VM's offers would be
  dropped.
- Not tested: a physical Linux host, or QEMU/OVMF.

## Answers to the brief's open questions

| Question | Answer |
|---|---|
| Path bootmgr requests for the BCD when booted directly over PXE | `\BCD` first (not found), then `\Boot\BCD` (tested 2026-11-05) |
| DHCP broadcast reception on Windows with a socket bound to the interface IP | **Works** (Hyper-V lab adapter, tested 2026-11-05) |
| Whether Hyper-V's Secure Boot templates trust the Windows UEFI CA 2023 | `MicrosoftWindows`: yes, 2023-signed boot manager boots (host build: ___). `MicrosoftUEFICertificateAuthority`: no Windows CA at all |
| Where recent Win11 media keeps the 2023-signed boot manager (`EFI_EX`) | Inside `sources\boot.wim` at `Windows\Boot\EFI_EX\bootmgfw_EX.efi` (2023 CA signed); the media's own `efi\boot\bootx64.efi` is still 2011 signed. Media build: ___ |
| UEFI HTTP Boot of a RAM-disk ISO | |
| TFTP `windowsize` support in the chosen Go TFTP library | Not in `pin/tftp/v3`; own server ([ADR 0012](../adr/0012-own-tftp-server.md)) |

## Decisions to revisit

ADRs to add or change because of these results.
