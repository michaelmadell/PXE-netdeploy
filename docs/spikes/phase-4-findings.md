# Phase 4: findings

Measured behaviour and facts established while building boot images,
the agent, profiles and deployments (from 2026-11-08). Earlier results:
[phase-3-findings.md](phase-3-findings.md). Decisions: ADR 0022 onwards.

## boot.wim contents (decision 1, ADR 0022)

Read-only probe (`go-wim` through our UDF reader) of the Phase 3 ISO
`CPBA_X64FRE_EN-GB_DV9`, tested 2026-11-08.

| Fact | Detail |
|---|---|
| boot.wim | 620,019,366 bytes, LZX, boot index 2 |
| Index 1 | "Microsoft Windows PE (amd64)", build 26100.9457, 2,445,011,470 bytes expanded |
| Index 2 | "Microsoft Windows Setup (amd64)", same build, 2,555,229,656 bytes expanded; has `setup.exe` and `sources/setup.exe` (index 1 has neither) |
| `winpeshl.ini` | In neither index; both have `Windows/System32/startnet.cmd`, `drvload.exe`, `wpeinit.exe` |
| Boot managers (both indexes) | `Windows/Boot/EFI/bootmgfw.efi` (3,087,400 bytes, 2011) and `Windows/Boot/EFI_EX/bootmgfw_EX.efi` (3,087,200 bytes, 2023) |
| boot.sdi | `Windows/Boot/DVD/EFI/boot.sdi`, 3,170,304 bytes, same size as the media's `boot/boot.sdi`; no `DVD_EX` copy (only `efisys_EX.bin`) |
| Fonts | `Windows/Boot/Fonts/*.ttf` (17) and `Windows/Boot/Fonts_EX/*_EX.ttf` (17, same names plus `_EX`, slightly different sizes). Media root `efi/microsoft/boot/fonts` matches the plain set |

Consequences: a boot image's files all come from the exported index;
default `sourceIndex` 1; the 2023 boot manager is served with the
`Fonts_EX` set renamed to the plain names (to verify in the lab: Phase 1
booted it with the plain set).

## Builders

| Fact | Detail |
|---|---|
| DISM on the dev machine | `10.0.26100.9549`, unelevated: capabilities report "needs elevation" |
| **DISM build, tested 2026-11-08** (`TestRealTools`, elevated, Win11 ISO index 1, placeholder agent) | Export 0.8 s; mount + copy + commit + re-export 43.3 s; boot.wim 581,705,960 bytes (the Phase 1 hand-exported index 1 was 581.7 MB, so the commit's dead space is gone); output has one LZX image, bootable; `auto` chose the 2023 boot manager (`EFI_EX/bootmgfw_EX.efi`, 3,087,200 bytes); boot.sdi 3,170,304 bytes; 16 fonts from `Fonts_EX`. The source was a copy of boot.wim in a temp folder, not a content-store blob |
| **wimlib build, tested 2026-11-08** (`TestRealTools`, unelevated, wimlib 1.14.5 Windows build in `.tmp\wimlib`) | Export 0.6 s; `update --rebuild --no-acls` 1.3 s (DISM: 43 s); boot.wim 582,993,799 bytes (1.3 MB larger than DISM's); same boot manager, boot.sdi and fonts |
| `-tags agent` server build | Embeds the agent; capabilities report `agent.architectures: [x64]` (tested 2026-11-08) |

## Lab boots (2026-11-08)

Both builds above (real agent via `NETDEPLOY_TEST_AGENT`, files written with
`NETDEPLOY_TEST_OUT`, the Phase 1 BCD at `Boot\BCD` asking 16384/16) served
with `-tftp-root` to `nd-win` (`MicrosoftWindows` Secure Boot template):

| Check | DISM build | wimlib build |
|---|---|---|
| Secure Boot boot with `bootmgfw_EX.efi` (2023) | Boots | Boots |
| Font | `\EFI\Microsoft\Boot\Fonts\wgl4_boot.ttf` served from the `Fonts_EX` set (50,120 bytes, 1 ms); boot screens normal | Same |
| `winpeshl.ini` → agent | Agent starts, loads `config.json`, logs its interface, then "deployment steps not implemented" | Same |

Answers: the 2023 boot manager works with the `Fonts_EX` set renamed to the
plain names (Phase 1 showed it also works with the plain set, so either is
fine; we keep `Fonts_EX`); files wimlib adds on Windows without security
descriptors (`--no-acls`) run in WinPE; a `winpeshl.ini` that launches only
the agent, which runs `wpeinit` itself, brings WinPE up.

## Our BCD (decision 2, ADR 0023)

Reading the Phase 1 store (`internal/bcd/testdata/phase1.bcd`, made with
`bcdedit`): hive 1.3, `lf` subkey lists, root `NewStoreRoot` with
`Description` (KeyName `BCD00000001`, FirmwareModified 1) and three objects:
`{bootmgr}` (description, default, displayorder, timeout 5), the WinPE loader
(device/osdevice `ramdisk=[boot]\Boot\boot.wim,{ramdiskoptions}` as a
170-byte element, path `\windows\system32\winload.efi`, systemroot
`\windows`, detecthal, winpe) and `{ramdiskoptions}` (ramdisksdidevice
`boot`, ramdisksdipath `\Boot\boot.sdi`, TFTP block 16384 and window 16 as
8-byte integers). The root key's parent offset points at unused space, so
loaders ignore it. The security descriptor's group was the creating user's
SID; ours uses Administrators.

**`bcdedit` and `reg load`, tested 2026-11-08** (`TestBcdedit`, elevated):
`bcdedit /store … /enum all /v` lists Windows Boot Manager, the Windows Boot
Loader (`ramdisk=[boot]\Boot\boot.wim,{ae5534e0-…}`, winpe Yes, detecthal
Yes) and Setup Ramdisk Options (sdi device `boot`, 16384/16) exactly as
intended; `reg load` accepts the hive.

**Lab boot with our BCD, tested 2026-11-08:** wimlib build of the Win11 ISO
(`.tmp\spike5`), BCD from `internal/bcd`, `nd-win` under Secure Boot. Boot
Manager loaded `\Boot\BCD` (12,288 bytes) and followed it: font, boot.sdi
and boot.wim requested with the BCD's 16384/16 (capped to 8192/8);
boot.wim 583,170,203 bytes in 2.6 s (224 MB/s, 0 retransmits); WinPE's
DHCP (`MSFT 5.0`) 4 s later, so the agent's `wpeinit` brought the network up.

## Boot decided per client (decision 3, ADR 0024)

**Tested 2026-11-08, `nd-win`:** ISO imported through the API, boot image
created and built (wimlib, `ready`, 601.5 MB of files), a profile using it
made `bootPrompt.defaultProfileId`. DHCP offered `bootmgfw.efi`; every TFTP
request carried `boot_session=dhcp boot_image=… profile=… via="default
profile"` and was served from the boot image's content-store tree; WinPE
started the agent. First boot-to-WinPE driven entirely by the API.

Two problems on the way:

- `POST /boot-images` returned 500 on success: the store error mapper read
  `err.Error()` of a nil error. Fixed; `TestCreateBootImage` covers it.
- WinPE restarted within seconds: the boot image held a 3-day-old
  `dist\agent-windows-amd64.exe` (the stub that read `agent.json`), which
  failed to find its config and exited, so WinPE restarted. Now `just agent`
  builds the agents, `just dev-server`/`just dev` run it first, the startup
  log shows the agents' modification time, and a failing agent waits two
  minutes in WinPE so its error can be read. Rebuilding with the new agent
  showed the boot image `stale` with reason `agent`, as designed.

## First deployment from the boot prompt (decision 4, ADR 0025)

**Tested 2026-11-08, `nd-client-01`** (the lab's `nd-win`; 64 GB virtual
disk added, content listener on :8080): boot prompt password, profile
menu, disk confirmation, then partitioning, download of `install.wim`,
`dism /Apply-Image`, configuring, `bcdboot`; the agent reported completion
and Windows setup ran from the disk. **Install worked.**

Open from this run:

- **After the deployment the VM rebooted to PXE**, not the installed
  Windows, with the profile's `disk-first`. bcdboot should have put Windows
  Boot Manager first in `BootOrder`; either it didn't from this WinPE, or
  Hyper-V kept the network first. To look at next: read `BootOrder` and the
  `Boot####` entries from WinPE (the agent's `firmware` package) before and
  after `bcdboot`, and set `BootOrder` ourselves for `disk-first`.
- Not yet checked: whether `bcdboot /bootex` was accepted (the deployment
  log says, `bcdboot /bootex failed` if not); the prompt timeout's BootNext
  to the local disk.
- A build was `failed` before this run; most likely `interrupted` by a
  server restart during the build (ADR 0016).

## Boot order after a deployment (2026-11-12)

**Tested on a second lab host** (`AH14195`, `just lab`, data on C:): one
deployment from the boot prompt with the agent logging the UEFI boot
variables. **Fixed: the VM restarted into Windows Setup.**

What the deployment log showed:

| When | BootOrder | Entries |
|---|---|---|
| Before the deployment | `0000,0004,0003,0001` | 0000 EFI Network; 0004 and 0003 "Windows Boot Manager" on partition `FB15A81F…`; 0001 EFI SCSI Device |
| After `bcdboot W:\Windows /s S: /f UEFI /bootex` | unchanged | unchanged |
| After the agent's boot order | `0002,0000,0004,0003,0001` | new 0002 "Windows Boot Manager" on the new system partition `B068F846…` (LBA 2048, 532,480 sectors) |

- **bcdboot run from a network-booted WinPE writes no firmware entries.**
  It reports "Boot files successfully created" and copies the files, but
  `BootOrder` and every `Boot####` stay as they were. `disk-first` relying
  on bcdboot's order could never work here.
- **Existing entries go stale on every redeploy:** diskpart makes a new
  system partition with a new GUID, and the old "Windows Boot Manager"
  entries point at a partition that no longer exists. Firmware skips them
  and falls through to the network: the reboot-to-PXE seen on 2026-11-08,
  and on this host after a deployment was interrupted mid-way.
- The agent now finds or writes the entry for the new partition and puts it
  first (ADR 0025, amended 2026-11-11).
- **`bcdboot /bootex` is accepted** in this WinPE: the installed system has
  the 2023-signed boot manager and boots under the `MicrosoftWindows`
  Secure Boot template.

**Stale entries, second run (2026-11-12, 11:05):** the agent now deletes
the entries of the partitions the deployment wiped. Before: `BootOrder`
`0000,0005,0002,0003,0001`, with 0005 and 0002 both on the previous system
partition `B068F846…` (the installed Windows had added 0005 for its own
partition on first boot), 0003 on the older `FB15A81F…`. After: new 0006 on
`F6A28140…` first, 0005 and 0002 deleted, `BootOrder` `0006,0000,0003,0001`.
0003 stays by design: its partition was already gone before the run, so the
agent can't tell it from a disk WinPE can't see. Hyper-V also has a hidden
`Boot0004 "FrontPage"` (its firmware UI) outside `BootOrder`.

Not yet checked: the prompt timeout's `BootNext` to the local disk.

To run once elevated, or with wimlib installed:

```powershell
$env:NETDEPLOY_TEST_ISO = 'C:\Users\monke\Downloads\Windows-11.iso'
go test -count=1 -run TestRealTools -v ./internal/bootimage
```

## Domain join from the built-in unattend (decision 5, ADR 0026)

**Tested 2026-11-16, `nd-client-01`, credentials join to the user's test
domain `michael-ad.test`** (DC `MICHAEL-AD`, NetBIOS `MICHAEL-DOM`, a second
adapter on `NetDeployLab` at `10.77.0.10`). **Joined** (`LAB-1-05` in
`OU=Lab`) after four problems, in the order they showed:

| Symptom | Cause | Fix |
|---|---|---|
| Computer named `LAB-`; not joined | The client was first seen by DHCP (no serial) and check-in never filled it in; `{{ .Client.Serial \| last 4 }}` rendered empty, and a trailing hyphen isn't a valid host name | Check-in fills missing serial/SMBIOS UUID; a pattern that uses an empty field, or a name with a hyphen at either end, is refused (`invalid_computer_name`) |
| Not joined | The lab DHCP handed out no DNS server: specialize couldn't find the domain | Pool `dnsServers` = the DC, `domainName` set; validate warns `dhcp_no_dns` |
| Every sign-in "incorrect", break-glass too | Not joined (below); the break-glass account was named `Administrator`, the built-in account Setup won't create (it stays disabled) | Validate refuses built-in names (`reserved_account_name`) |
| `NetSetup.LOG`: `NetUseAdd … IPC$ returned 1326`, `setupact.log`: `Unattended Join … 0x52e` | The join secret's password was wrong (the DC was found and reached) | Secret re-entered after checking it with `net use \\DC\IPC$ /user:…` |

Notes:
- The join account `michael@michael-ad.test` reaches the unattend as
  `Domain michael-ad.test`, `Username michael`; Setup logs
  `Account: michael-ad.test\michael`. Either the UPN or `NETBIOS\user` works.
- Setup retries the join every 10 s for about 4.5 minutes, then carries on
  unjoined (`Unattended Join: Exit, returning 0x0`): a failed join doesn't
  fail Setup, so it only shows at sign-in.
- Reading the logs without an account: mount the VM's VHD on the host
  (read-write, VM off), pick the largest partition (diskpart's volume labels
  didn't survive), read `Windows\debug\NetSetup.LOG` and
  `Windows\Panther\UnattendGC\setupact.log`.

Not yet tested: offline join (ODJ), `local` identity end to end, arming from
the API, cancelling in the lab.
