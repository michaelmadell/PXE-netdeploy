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

## Task results

| # | Task | Result | Notes |
|---|---|---|---|
| 1 | DHCP, full mode | | |
| 2 | TFTP | | |
| 3 | WinPE on Windows-CA VM | | |
| 4 | WinPE on third-party-CA VM | | |
| 5 | 2023-signed boot manager | | |
| 6 | Transfer tuning | | |
| 7 | I210, stock WinPE | | |
| 8 | I210, driver injected | | |
| 9 | HTTP Boot ISO (optional) | | |
| 10 | Linux host | | |

## TFTP requests seen

Every path Boot Manager and WinPE asked for, in order, and whether it was found.

| # | Requested path | Options | Result |
|---|---|---|---|
| | | | |

## BCD and file layout

Differences from the BCD recipe in the plan, and the final layout.

## Secure Boot results

| Boot manager | Signer | nd-win (Windows CA) | nd-shim (+ third-party CA) | I210 device |
|---|---|---|---|---|
| `bootx64.efi` from media | | | | |
| `bootmgfw_EX.efi` | | | | |

## Transfer timings (boot.wim, size: ___ MB)

| blksize / windowsize | Duration | MB/s | Retransmits |
|---|---|---|---|
| 1456 / 1 | | | |
| 1456 / 8 | | | |
| 8192 / 8 | | | |
| 16384 / 16 | | | |

Chosen defaults:

## I210

## HTTP Boot

## Linux host

## Answers to the brief's open questions

| Question | Answer |
|---|---|
| Path bootmgr requests for the BCD when booted directly over PXE | |
| DHCP broadcast reception on Windows with a socket bound to the interface IP | |
| Whether Hyper-V's Secure Boot templates trust the Windows UEFI CA 2023 | |
| Where recent Win11 media keeps the 2023-signed boot manager (`EFI_EX`) | |
| UEFI HTTP Boot of a RAM-disk ISO | |
| TFTP `windowsize` support in the chosen Go TFTP library | Not in `pin/tftp/v3`; own server ([ADR 0012](../adr/0012-own-tftp-server.md)) |

## Decisions to revisit

ADRs to add or change because of these results.
