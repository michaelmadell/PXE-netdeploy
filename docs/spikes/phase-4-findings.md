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

To run once elevated, or with wimlib installed:

```powershell
$env:NETDEPLOY_TEST_ISO = 'C:\Users\monke\Downloads\Windows-11.iso'
go test -count=1 -run TestRealTools -v ./internal/bootimage
```
