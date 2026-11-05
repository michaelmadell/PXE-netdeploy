# Phase 1 spike: Secure Boot network boot of WinPE

## Goal

Prove the riskiest part of the design before building the pipeline around it:
a Windows 11 WinPE image boots **under Secure Boot** from **our own** DHCP and
TFTP, on stock Microsoft keys, and then on real hardware with an Intel I210 NIC.

Spike code goes in real packages (`internal/dhcp`, `internal/tftp`) but stays
minimal: hard-coded or flag-driven config, one interface, no database. Phase 2
hardens it. Everything learned goes into a findings document.

## Out of scope

Image import, boot image builder, agent behaviour, the API beyond health, the
UI, proxy DHCP mode, HTTPS. (HTTP Boot is tested by hand, not implemented.)

## Setup

1. `just lab` (elevated) for the Windows-CA-only VM, and
   `just lab -VMName nd-shim -SecureBootTemplate MicrosoftUEFICertificateAuthority`
   for the second trust configuration.
2. Prepare boot files by hand from a current Windows 11 ISO, into a spike
   directory served as the TFTP root:
   - `bootmgfw.efi`: the media's `efi\boot\bootx64.efi`
   - `Boot\boot.sdi`: from the media's `boot\boot.sdi`
   - `Boot\boot.wim`: the media's `sources\boot.wim`, unmodified
   - `Boot\BCD`: built with `bcdedit` as below
3. Also extract the 2023-signed boot manager, if present, from inside the WIMs
   (`Windows\Boot\EFI_EX\bootmgfw_EX.efi`) for task 5.

### BCD for a TFTP RAM disk (Windows host, elevated)

```powershell
$s = 'BCD'
bcdedit /createstore $s
bcdedit /store $s /create '{ramdiskoptions}' /d 'Ramdisk options'
bcdedit /store $s /set '{ramdiskoptions}' ramdisksdidevice boot
bcdedit /store $s /set '{ramdiskoptions}' ramdisksdipath '\Boot\boot.sdi'
bcdedit /store $s /set '{ramdiskoptions}' ramdisktftpblocksize 1456
bcdedit /store $s /set '{ramdiskoptions}' ramdisktftpwindowsize 8

$out = bcdedit /store $s /create /d 'WinPE' /application osloader
$id = [regex]::Match($out, '\{[0-9a-fA-F-]+\}').Value
bcdedit /store $s /set $id device 'ramdisk=[boot]\Boot\boot.wim,{ramdiskoptions}'
bcdedit /store $s /set $id osdevice 'ramdisk=[boot]\Boot\boot.wim,{ramdiskoptions}'
bcdedit /store $s /set $id path '\windows\system32\winload.efi'
bcdedit /store $s /set $id systemroot '\windows'
bcdedit /store $s /set $id detecthal Yes
bcdedit /store $s /set $id winpe Yes

bcdedit /store $s /create '{bootmgr}' /d 'Boot Manager'
bcdedit /store $s /set '{bootmgr}' timeout 5
bcdedit /store $s /displayorder $id
bcdedit /store $s /default $id
```

This is the standard RAM-disk recipe; check it against current Microsoft
documentation if anything fails, and record differences in the findings.

## Tasks

| # | Task | Done when |
|---|---|---|
| 1 | **DHCP, full mode, one interface.** Pool on the lab subnet; answer UEFI x64 clients (option 93 = 7 or 9) with next-server and boot file `bootmgfw.efi`. Log every packet's MAC, arch and message type. | VM gets an address and starts downloading the boot file |
| 2 | **TFTP, read-only.** Serve a directory, case-insensitive, `\` → `/` normalised, `blksize` and `tsize` options, `windowsize` if the library supports it. Log every requested path and transfer time. | Boot manager loads and requests further files |
| 3 | **Boot WinPE (Windows-CA-only VM).** Follow the requested paths; adjust layout or BCD until WinPE starts. | Windows Setup screen appears |
| 4 | **Repeat on the third-party-CA VM.** | Recorded result. *Refused, as expected: that template removes the Windows CA (see findings).* |
| 5 | **2023-signed boot manager.** Serve `bootmgfw_EX.efi` instead. | Recorded whether each VM template accepts it |
| 6 | **Transfer tuning.** Time the boot.wim download at a few block/window sizes (e.g. 1456/1, 1456/8, 8192/8, 16384/16). | Table of results; default settings chosen |
| 7 | **Real I210 hardware.** Boot the same files on a physical I210 device on an isolated switch. Expect WinPE to have no network. | Confirmed (or disproved) that WinPE lacks a working I210 driver |
| 8 | **Inject the I210 driver** into a copy of boot.wim with `DISM /Add-Driver`; boot again. | `ipconfig` in WinPE shows a lease from our DHCP |
| 9 | **HTTP Boot ISO (optional).** Wrap bootmgr + BCD + boot.wim in a small UEFI-bootable ISO and point an HTTP Boot client at it (QEMU/OVMF or the real device). | Recorded whether WinPE starts from the RAM-disk ISO |
| 10 | **Linux host.** Run tasks 1–3 with netdeployd on Linux (bridge or tap to a QEMU/OVMF VM). | Same result as task 3 |

## Implementation notes

- **DHCP library:** start with `github.com/insomniacslk/dhcp` (`dhcpv4`, `server4`).
  Its server binds to a device on Linux; check its behaviour on Windows. If
  needed, use a plain UDP socket on the interface address and broadcast
  replies to `255.255.255.255:68`.
- **TFTP library:** `github.com/pin/tftp/v3` turned out not to support
  `windowsize` (RFC 7440), so `internal/tftp` is our own read-only server
  ([ADR 0012](../adr/0012-own-tftp-server.md)).
- **Elevation:** on Windows, ports 67 and 69 don't need elevation, but firewall
  rules do (`just lab` adds them). On Linux, binding them needs root or
  `CAP_NET_BIND_SERVICE`.
- **Port conflicts:** stop Internet Connection Sharing (SharedAccess) if UDP 67
  is taken; WDS, if installed, holds 69 and 4011.

## Running it

Step-by-step commands for each task, and what to capture:
[spikes/phase-1-runbook.md](../spikes/phase-1-runbook.md). Results go in
[spikes/phase-1-findings.md](../spikes/phase-1-findings.md).

## Deliverables

- `internal/dhcp` and `internal/tftp` spike implementations, behind flags on
  `netdeployd`, with the request logging above.
- `docs/spikes/phase-1-findings.md`: what was requested, what worked, timings,
  Secure Boot results per template, I210 results, HTTP Boot result.
- ADR updates where findings change a decision (for example, a TFTP library
  choice, or if BCD paths force a different file layout).
- Answers to the "Open questions" table in `PROJECT_BRIEF.md`.
