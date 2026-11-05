# Phase 1 spike: runbook

Commands for each task in [the plan](../plan/phase-1-spike.md). Record results
in [phase-1-findings.md](phase-1-findings.md). Every run's log is evidence:
keep them under `.tmp/logs/` (gitignored) and paste the relevant lines into
the findings.

Commands are PowerShell 7 on the Windows host, from the repo root, unless a
task says otherwise.

## 0. One-time preparation

### Dependencies

The DHCP spike uses `github.com/insomniacslk/dhcp`:

```powershell
go get github.com/insomniacslk/dhcp@latest
go mod tidy
just check-drift; just lint; just test
```

Commit `go.mod` and `go.sum` with the spike code.

### Lab and ports

```powershell
just lab                                                                    # nd-win: Windows CA only (elevated)
just lab -VMName nd-shim -SecureBootTemplate MicrosoftUEFICertificateAuthority
Get-NetUDPEndpoint -LocalPort 67, 69 -ErrorAction SilentlyContinue          # must be empty
```

If 67 is taken, stop Internet Connection Sharing: `Stop-Service SharedAccess`.
If 69 is taken, WDS or another TFTP server is running.

### Spike TFTP root

```powershell
$iso = 'C:\path\to\Win11.iso'
$root = '.tmp\spike'
$d = (Mount-DiskImage $iso -PassThru | Get-Volume).DriveLetter + ':'
New-Item -ItemType Directory -Force "$root\Boot\Fonts", .tmp\logs | Out-Null
Copy-Item "$d\efi\boot\bootx64.efi" "$root\bootmgfw.efi"
Copy-Item "$d\boot\boot.sdi"       "$root\Boot\boot.sdi"
Copy-Item "$d\sources\boot.wim"    "$root\Boot\boot.wim"
Copy-Item "$d\efi\microsoft\boot\fonts\*" "$root\Boot\Fonts\"   # Boot Manager may ask for these
```

Build `Boot\BCD` with the `bcdedit` script in the plan (elevated, run inside
`$root\Boot`).

For task 5, find the 2023-signed boot manager:

```powershell
dism /Get-WimInfo /WimFile:$root\Boot\boot.wim
New-Item -ItemType Directory -Force .tmp\wim | Out-Null
Mount-WindowsImage -ImagePath "$root\Boot\boot.wim" -Index 2 -Path .tmp\wim -ReadOnly
Get-ChildItem .tmp\wim\Windows\Boot\EFI_EX -ErrorAction SilentlyContinue
Copy-Item .tmp\wim\Windows\Boot\EFI_EX\bootmgfw_EX.efi "$root\bootmgfw_EX.efi"
Dismount-WindowsImage -Path .tmp\wim -Discard
```

Record the index used and whether `EFI_EX` exists (an open question).
Check the signer of each boot manager:
`Get-AuthenticodeSignature $root\bootmgfw*.efi | Format-List SignerCertificate`
(the issuer shows "Windows Production PCA 2011" or "Windows UEFI CA 2023").

## Tasks 1–3: boot WinPE on nd-win

```powershell
go run ./cmd/netdeployd `
  -dhcp-iface 'vEthernet (NetDeployLab)' -dhcp-pool 10.77.0.100-10.77.0.199 `
  -tftp-root .tmp\spike -tftp-addr 10.77.0.1:69 `
  *>&1 | Tee-Object .tmp\logs\task3-nd-win.log
```

In another elevated shell: `Start-VM nd-win; vmconnect localhost nd-win`.

What to look for in the log, in order:

| Line | Meaning |
|---|---|
| `dhcp listener started` | Note `bind=10.77.0.1:67` |
| `dhcp rx type=DISCOVER arch=7(...)` | Broadcasts reach a socket bound to the interface IP (open question) |
| `dhcp tx type=OFFER ... boot=pxe-uefi-x64 file=bootmgfw.efi` | Offer sent |
| `dhcp rx type=REQUEST`, `dhcp tx type=ACK` | Lease bound |
| `tftp request path=bootmgfw.efi` | Task 1 done |
| `tftp transfer complete ... resolved=bootmgfw.efi` | Task 2 done |
| `tftp request path=...BCD` | **The exact path** Boot Manager asks for (open question) |
| `tftp request refused` | Missing files: fonts, `.mui` resources. Add them or note them |
| `tftp transfer complete resolved=Boot/boot.wim ... duration_ms` | RAM disk loaded; note blksize, windowsize, duration |
| `dhcp rx` without `arch` after boot | WinPE's own DHCP client |

Done when the Windows Setup screen appears.

If the VM shows "No offer received", try in this order and note which fixed it:

1. Check Windows Firewall: `Get-NetFirewallRule -Group 'netdeploy lab'`.
2. Restart with `-dhcp-pxeclient-class`.
3. Bind TFTP to all interfaces: `-tftp-addr 0.0.0.0:69`.

If boot.wim stalls or restarts at about 95 MB (block 65535 at blksize 1456),
block-number rollover is the suspect; record the log lines around it.

## Task 4: nd-shim

Same command, log to `.tmp\logs\task4-nd-shim.log`, `Start-VM nd-shim`.

## Task 5: 2023-signed boot manager

Restart with `-boot-file bootmgfw_EX.efi` and boot each VM in turn. Record per
template: accepted, or the firmware's Secure Boot violation message.

## Task 6: transfer tuning

Boot Manager asks for the blksize and windowsize set in the BCD; the server
can only lower them. Set the BCD high once, then cap from the server:

```powershell
bcdedit /store .tmp\spike\Boot\BCD /set '{ramdiskoptions}' ramdisktftpblocksize 16384
bcdedit /store .tmp\spike\Boot\BCD /set '{ramdiskoptions}' ramdisktftpwindowsize 16
```

| Run | Server flags |
|---|---|
| 1456 / 1 | `-tftp-max-blksize 1456 -tftp-max-windowsize 1` |
| 1456 / 8 | `-tftp-max-blksize 1456 -tftp-max-windowsize 8` |
| 8192 / 8 | `-tftp-max-blksize 8192 -tftp-max-windowsize 8` |
| 16384 / 16 | (no caps) |

Take `duration_ms`, `mb_per_s` and `retransmits` from the boot.wim
`tftp transfer complete` line. Turn the VM off between runs.

## Task 7: real I210 hardware

Put a host NIC and the device on an isolated switch. Give the host NIC a
static `10.77.0.1/24` and allow UDP 67/69 on it (or reuse the lab rules by
adding its alias). Run with `-dhcp-iface '<that NIC alias>'`
(`Get-NetAdapter` lists aliases). Boot the device from its network boot
entry with Secure Boot on.

In WinPE (Shift+F10 on the Setup screen): `ipconfig /all`. Expect no
I210 adapter or no address, and no `dhcp rx` without `arch` in the log.

## Task 8: inject the I210 driver

Get the I210 driver (`e1r*.inf`) from Intel's Ethernet driver package for
Windows 11.

```powershell
Copy-Item .tmp\spike\Boot\boot.wim .tmp\spike\Boot\boot-orig.wim
Mount-WindowsImage -ImagePath .tmp\spike\Boot\boot.wim -Index 2 -Path .tmp\wim
Add-WindowsDriver -Path .tmp\wim -Driver C:\path\to\e1r68x64.inf
Dismount-WindowsImage -Path .tmp\wim -Save
```

Boot again. Done when `ipconfig` in WinPE shows a `10.77.0.x` lease and the
log shows the matching `dhcp tx type=ACK`.

## Task 9 (optional): HTTP Boot ISO

Hyper-V Gen 2 has no HTTP Boot; use the real device or QEMU/OVMF.

1. Build a WinPE ISO with the ADK (Deployment Tools environment):
   `copype amd64 C:\WinPE_amd64`, add the I210 driver to its `boot.wim`,
   then `MakeWinPEMedia /ISO C:\WinPE_amd64 .tmp\spike\winpe.iso`.
2. Serve it over HTTP. The content listener isn't built yet, so any static
   server will do, e.g. `python -m http.server 80 --directory .tmp\spike`
   (allow TCP 80 on the NIC).
3. Run netdeployd with `-http-boot-url http://10.77.0.1/winpe.iso`.
4. Choose the firmware's HTTP Boot (IPv4) entry.

Look for `dhcp rx ... arch=16(...) class=HTTPClient...` and
`boot=http-uefi-x64`. Record whether the firmware downloads the ISO and
whether WinPE starts from the RAM disk.

## Task 10: Linux host

```sh
GOOS=linux go build -o .tmp/netdeployd ./cmd/netdeployd   # or build on the Linux host
sudo .tmp/netdeployd -dhcp-iface br-nd -dhcp-pool 10.77.0.100-10.77.0.199 \
  -tftp-root .tmp/spike 2>&1 | tee .tmp/logs/task10-linux.log
```

`br-nd` is a bridge with `10.77.0.1/24` and the VM's tap attached. Run QEMU
with `OVMF_CODE.secboot.fd`, a copy of the Microsoft-keys vars file, a
`virtio-net` or `e1000e` NIC on the tap, and network boot first. On Linux the
DHCP socket binds `0.0.0.0:67` to the device, so note any difference from
Windows.

## What to send back

For each task: the log file (or the lines from the table above), what
appeared on screen, and anything you changed to get it working.
