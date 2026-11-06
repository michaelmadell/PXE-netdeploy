# Phase 2: lab findings

Measured behaviour from Phase 2 lab runs. Procedures:
[running-the-lab.md](../dev/running-the-lab.md). Phase 1 results:
[phase-1-findings.md](phase-1-findings.md).

## Proxy DHCP against Hyper-V firmware

**Tested 2026-11-06. Pass:** WinPE booted under Secure Boot with netdeployd in
proxy mode and an unrelated DHCP server handing out addresses.

| Item | Value |
|---|---|
| Real DHCP server | dnsmasq on `nd-linux` (`10.77.0.2`), no PXE options |
| netdeployd | Windows host, `vEthernet (NetDeployLab)` = `10.77.0.1`, proxy mode on `:67` and `:4011`, fresh data dir |
| Client | Hyper-V Gen 2 VM (`MicrosoftWindows` template), MAC `00:15:5d:00:85:09` |
| Boot file | `bootmgfw_EX.efi` (2023-signed), BCD ramdisk 16384/16, server caps 8192/8 |
| Log | `.tmp\logs\proxy-nd-win.log` (not committed) |

Exchange (times from the log):

| t (s) | From | What |
|---|---|---|
| 0.000 | firmware | DISCOVER on :67, option 60 `PXEClient:Arch:00007:UNDI:003000`, option 93 = 7 |
| 0.002 | netdeployd | Proxy OFFER to 255.255.255.255:68: no address, `siaddr` 10.77.0.1, file `bootmgfw_EX.efi`; client recorded as `discovered` |
| 0 – 3.9 | dnsmasq | OFFER (address 10.77.0.140) |
| 3.943 | firmware | REQUEST on :67 to dnsmasq (`server_id` 10.77.0.2, requested 10.77.0.140). netdeployd: no reply, noted 10.77.0.140 for boot |
| 3.949 | firmware | TFTP `bootmgfw_EX.efi` from 10.77.0.140 (tsize probe, then `blksize=1482 windowsize=4`) |
| 4.1 – 7.2 | Boot Manager | Same TFTP sequence as Phase 1: `\BCD` (missing), `\Boot\BCD`, optional policy files, own image, font, boot.sdi + boot.wim in parallel |
| 12.0 | WinPE | DHCP (`MSFT 5.0`, no option 93): netdeployd ignored it, dnsmasq answered |

Findings:

- **The firmware takes the boot file straight from the proxy offer.** It sent
  no REQUEST to :4011, so `PXE_DISCOVERY_CONTROL = 0x08` (option 43) is
  enough for Hyper-V. Keep :4011 for firmware that does boot server
  discovery; it is untested against real firmware.
- **The client's REQUEST to the real server gives its IP** before TFTP
  starts (6 ms earlier here), so the boot session is ready for per-client
  files (step 4).
- **Option 97 byte order is right.** The recorded `smbiosUuid`
  `8ac5df62-35cd-47c7-a2e5-7a0d611c246a` equals the VM's `BIOSGUID`
  (`Msvm_VirtualSystemSettingData`), so the first three fields are
  little-endian on the wire, as assumed.
- **WinPE's own DHCP is correctly left to the real server** (not a PXE
  client, no proxy reply).
- boot.wim: 581.7 MB in 2.74 s (212 MB/s), 0 retransmits, through the proxy
  path; the same as full mode in Phase 1.
- **Bug found and fixed:** the client's `lastIp` stayed empty because the
  30 s sighting throttle dropped the REQUEST that carried the address 4 s
  after the DISCOVER. A sighting with a new address is now always written
  (`TestProxySightingKeepsAddressFromRequest`).

Still untested:

- `:4011` boot server REQUEST/ACK with real firmware (Hyper-V doesn't use it).
- `listenOn67: false`.
- The ICMP ping check on Windows (full mode; not exercised by this run).
- Physical hardware and other firmware (I210 device, Phase 1 tasks 7–8).

Other observations:

- `GET /clients` returned 500 because the operation wasn't implemented yet
  (the scaffolding panics and `Recover` logs the stack). Implemented the same
  day.
- ~20 "tftp request refused" warnings per boot for optional files, as in
  Phase 1. Since step 4 they are logged at debug.

## Decision: no adaptive TFTP window (2026-11-06)

Phase 1 suggested shrinking the window after a lost block. Not done: under
RFC 7440 the receiver ACKs after a full window or when its own timeout
expires. If the server sends fewer blocks than the negotiated window, Boot
Manager waits out its timeout before ACKing, every window, which costs more
than the loss it was meant to avoid. The negotiated window can't be changed
mid-transfer. Revisit with the I210 timing runs (Phase 1 tasks 7–8): if loss
is common on real hardware, the fix is a lower `maxWindowSize`, not a dynamic
one.

## Full mode regression after step 4 (Hyper-V, 2026-11-06)

**Pass.** Same VM (`00:15:5d:00:85:09`), full mode on `vEthernet (NetDeployLab)`,
netdeployd restarted between two boots. Log: `.tmp\logs\step4-full.log`
(second run only; the first was overwritten).

- **Leases survive a restart:** `dhcp leases loaded count=1` at startup; the
  firmware and then WinPE got the same `10.77.0.100` back.
- **Per-client TFTP selection on a real boot:** every TFTP request logged
  `mac=00:15:5d:00:85:09 boot_session=dhcp`.
- **Optional-file misses are quiet:** all 17 (`\BCD`, `.p7b` policies,
  `boot.stl` twice, `UnlockToken.pol`) at debug; no TFTP warnings.
- boot.wim 581.7 MB in 2.66 s (218.5 MB/s), 0 retransmits; WinPE started.
- **Not observed:** `boot_session=lease` (needs netdeployd restarted between a
  client's DHCP and its TFTP; covered by `TestClientRoot`), and the ICMP ping
  check, because `.100` already had a lease and isn't pinged. The ping check on
  Windows is checked by `go test -run TestICMPPinger -v ./internal/dhcp` in an
  elevated shell: **PASS** (2026-11-06, Windows host, elevated): raw ICMP
  works, `127.0.0.1` answers and a TEST-NET address doesn't, so the Go net
  package strips the IPv4 header on Windows too and full mode's ping check is
  real there.
