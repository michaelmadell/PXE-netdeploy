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
Get-NetUDPEndpoint -LocalPort 67, 69 -ErrorAction SilentlyContinue |
  Select-Object LocalAddress, LocalPort, OwningProcess
```

Only a socket on `0.0.0.0` or `10.77.0.1` conflicts with netdeployd. A socket
on another adapter's address is fine: the Hyper-V Default Switch's DHCP
(Internet Connection Sharing, `SharedAccess`) listens on its own address,
e.g. `172.31.224.1:67`. Leave it running; stopping it breaks networking for
VMs on the Default Switch. If 69 is taken, WDS or another TFTP server is
running.

### Spike TFTP root

```powershell
$iso = 'C:\path\to\Win11.iso'
$root = '.tmp\spike'
$d = (Mount-DiskImage $iso -PassThru | Get-Volume).DriveLetter + ':'
New-Item -ItemType Directory -Force "$root\Boot", "$root\EFI\Microsoft\Boot\Fonts", .tmp\logs | Out-Null
Copy-Item "$d\efi\boot\bootx64.efi" "$root\bootmgfw.efi"
Copy-Item "$d\boot\boot.sdi"       "$root\Boot\boot.sdi"
Copy-Item "$d\sources\boot.wim"    "$root\Boot\boot.wim"
Copy-Item "$d\efi\microsoft\boot\fonts\*" "$root\EFI\Microsoft\Boot\Fonts\"   # Boot Manager asks for \EFI\Microsoft\Boot\Fonts\*.ttf
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
Check the signer embedded in each boot manager. Don't rely on
`Get-AuthenticodeSignature` alone: if Windows has the file's hash in a
catalog it reports the catalog signer (2011), not the embedded signature
that UEFI checks.

```powershell
$files = Get-ChildItem $root\bootmgfw*.efi
Get-FileHash $files | Select-Object Hash, Path
$files | ForEach-Object {
  [pscustomobject]@{
    File   = $_.Name
    Issuer = [Security.Cryptography.X509Certificates.X509Certificate]::CreateFromSignedFile($_.FullName).Issuer
  }
}
```

The issuer is "Microsoft Windows Production PCA 2011" or "Windows UEFI CA 2023".
Signing-certificate expiry dates don't matter to Secure Boot.

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

Done when the Windows Setup screen appears. Then build the
[plain WinPE boot image](#plain-winpe-boot-image-after-task-3) below.

If the VM shows "No offer received", try in this order and note which fixed it:

1. Check Windows Firewall: `Get-NetFirewallRule -Group 'netdeploy lab'`.
2. Restart with `-dhcp-pxeclient-class`.
3. Bind TFTP to all interfaces: `-tftp-addr 0.0.0.0:69`.

If boot.wim stalls or restarts at about 95 MB (block 65535 at blksize 1456),
block-number rollover is the suspect; record the log lines around it.

## Plain WinPE boot image (after task 3)

Setup on the media's `boot.wim` shows "Windows Deployment Services client
functionality is being partly deprecated" and restarts when you click OK.
That is expected: since Windows 11, Microsoft blocks running the media's
Setup when network-booted ("WDS mode"); custom boot images are unaffected
(https://aka.ms/WDSSupport). Tasks 4 onwards use plain WinPE, which is
index 1 of the same WIM and boots to a command prompt.

Elevated PowerShell, repo root:

```powershell
# 1. Index 1 should be "Microsoft Windows PE", index 2 "Microsoft Windows Setup"
Get-WindowsImage -ImagePath .tmp\spike\Boot\boot.wim | Select-Object ImageIndex, ImageName, ImageSize

# 2. Keep the original; export index 1 as the new, bootable boot.wim
Rename-Item .tmp\spike\Boot\boot.wim boot-setup.wim
Export-WindowsImage -SourceImagePath .tmp\spike\Boot\boot-setup.wim -SourceIndex 1 `
  -DestinationImagePath .tmp\spike\Boot\boot.wim -SetBootable

# 3. Expect one image and a smaller file
Get-WindowsImage -ImagePath .tmp\spike\Boot\boot.wim | Select-Object ImageIndex, ImageName
(Get-Item .tmp\spike\Boot\boot.wim).Length / 1MB
```

The BCD doesn't change (same file name). Boot nd-win again with the task 3
command: a command prompt (`X:\windows\system32\cmd.exe`) should appear, and
`ipconfig` should show a `10.77.0.x` lease from netdeployd. Note the new
boot.wim size for task 6.

## Task 4: nd-shim

Same command, log to `.tmp\logs\task4-nd-shim.log`, `Start-VM nd-shim`.

Expected result: Secure Boot violation. The `MicrosoftUEFICertificateAuthority`
template trusts only the third-party CA and removes the Windows CA, so the
firmware refuses `bootmgfw.efi`. Evidence to keep: the log shows the
`bootmgfw.efi` transfer complete and no request for the BCD afterwards.

## Task 5: 2023-signed boot manager

Restart with `-boot-file bootmgfw_EX.efi` and boot each VM in turn. Record per
template: accepted, or the firmware's Secure Boot violation message.

## Task 6: transfer tuning

Boot Manager asks for the blksize and windowsize set in the BCD; the server
can only lower them. The server's default caps are 8192/8 (chosen from these
runs), so pass explicit caps for every run. Set the BCD high once:

```powershell
bcdedit /store .tmp\spike\Boot\BCD /set '{ramdiskoptions}' ramdisktftpblocksize 16384
bcdedit /store .tmp\spike\Boot\BCD /set '{ramdiskoptions}' ramdisktftpwindowsize 16
```

| Run | Server flags |
|---|---|
| 1456 / 1 | `-tftp-max-blksize 1456 -tftp-max-windowsize 1` |
| 1456 / 8 | `-tftp-max-blksize 1456 -tftp-max-windowsize 8` |
| 8192 / 8 | (defaults) |
| 16384 / 16 | `-tftp-max-blksize 16384 -tftp-max-windowsize 16` (fails on Hyper-V) |

Take `duration_ms`, `mb_per_s` and `retransmits` from the boot.wim
`tftp transfer complete` line. Turn the VM off between runs.

## Task 7: real I210 hardware

Put a host NIC and the device on an isolated switch. Give the host NIC a
static `10.77.0.1/24` and allow UDP 67/69 on it (or reuse the lab rules by
adding its alias). Run with `-dhcp-iface '<that NIC alias>'`
(`Get-NetAdapter` lists aliases). Boot the device from its network boot
entry with Secure Boot on.

At the WinPE command prompt: `ipconfig /all`. Expect no I210 adapter or no
address, and no `dhcp rx` without `arch` in the log.

Also repeat the task 6 timings here: physical networks handle IP fragments
worse than Hyper-V's switch, and these runs set the final defaults. The boot.wim
download doesn't need the I210 driver (the firmware and Boot Manager use their
own network stack).

| Run | Server flags | Why |
|---|---|---|
| 1456 / 8 | `-tftp-max-blksize 1456 -tftp-max-windowsize 8` | Unfragmented baseline |
| 1456 / 16 | `-tftp-max-blksize 1456 -tftp-max-windowsize 16` | Unfragmented, more in flight |
| 1456 / 32 | `-tftp-max-blksize 1456 -tftp-max-windowsize 32` | Unfragmented, more in flight |
| 8192 / 8 | (defaults) | Current default |

If a run fails, note Boot Manager's on-screen error and the `retransmits` count.

## Task 8: inject the I210 driver

Get the I210 driver (`e1r*.inf`) from Intel's Ethernet driver package for
Windows 11.

```powershell
Copy-Item .tmp\spike\Boot\boot.wim .tmp\spike\Boot\boot-orig.wim
Mount-WindowsImage -ImagePath .tmp\spike\Boot\boot.wim -Index 1 -Path .tmp\wim
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

Run netdeployd in a Linux VM attached to the `NetDeployLab` switch and boot
nd-win from it. Same client as task 3, so any difference is the Linux server
path: DHCP binds `0.0.0.0:67` with `SO_BINDTODEVICE` instead of the
interface address.

### 10.1 Linux VM (once, elevated PowerShell)

Any current Ubuntu Server LTS ISO works. Two NICs: Default Switch for
internet (install, copying files), `NetDeployLab` for serving.

```powershell
$vm = 'nd-linux'
New-VM -Name $vm -Generation 2 -MemoryStartupBytes 2GB -SwitchName 'Default Switch' `
  -NewVHDPath (Join-Path (Get-VMHost).VirtualHardDiskPath "$vm.vhdx") -NewVHDSizeBytes 20GB
Add-VMNetworkAdapter -VMName $vm -SwitchName NetDeployLab -Name lab
# Ubuntu's boot loader is signed by the third-party CA (shim), which the
# MicrosoftWindows template doesn't trust:
Set-VMFirmware -VMName $vm -SecureBootTemplate MicrosoftUEFICertificateAuthority
Add-VMDvdDrive -VMName $vm -Path C:\path\to\ubuntu-server.iso
Set-VMFirmware -VMName $vm -FirstBootDevice (Get-VMDvdDrive -VMName $vm)
# Hyper-V's DHCP Guard drops DHCP offers from a VM; it must be Off (the default):
Get-VMNetworkAdapter -VMName $vm | Select-Object Name, SwitchName, MacAddress, DhcpGuard
Start-VM $vm; vmconnect localhost $vm
```

Install with OpenSSH server enabled; let the Default Switch NIC use DHCP and
leave the lab NIC unconfigured. After install, find which interface is the lab
NIC by MAC (`ip -br link` in the VM vs `MacAddress` above; usually `eth1`) and
give it a static address with no DHCP client:

```sh
sudo tee /etc/netplan/60-lab.yaml >/dev/null <<'EOF2'
network:
  version: 2
  ethernets:
    eth1:
      dhcp4: false
      addresses: [10.77.0.2/24]
EOF2
sudo chmod 600 /etc/netplan/60-lab.yaml
sudo netplan apply
ip -br addr show eth1      # expect 10.77.0.2/24
```

`10.77.0.2` because the host keeps `10.77.0.1` on the same switch.

### 10.2 Copy the binary and boot files (PowerShell, repo root)

Stop netdeployd on the Windows host first: only one DHCP server on the switch.

```powershell
$env:GOOS = 'linux'; $env:GOARCH = 'amd64'
go build -o .tmp\linux\netdeployd ./cmd/netdeployd
Remove-Item Env:GOOS, Env:GOARCH

$ip = '<nd-linux Default Switch address>'   # `ip -br addr` in the VM
ssh user@$ip 'mkdir -p ~/spike/Boot ~/spike/EFI/Microsoft/Boot/Fonts'
scp .tmp\linux\netdeployd user@${ip}:~/
scp .tmp\spike\bootmgfw.efi .tmp\spike\bootmgfw_EX.efi user@${ip}:~/spike/
scp .tmp\spike\Boot\BCD .tmp\spike\Boot\boot.sdi .tmp\spike\Boot\boot.wim user@${ip}:~/spike/Boot/
scp .tmp\spike\EFI\Microsoft\Boot\Fonts\wgl4_boot.ttf user@${ip}:~/spike/EFI/Microsoft/Boot/Fonts/
```

(`boot-setup.wim` isn't needed.)

### 10.3 Run and boot

In the VM:

```sh
chmod +x ~/netdeployd
sudo ~/netdeployd -data ~/nd-data \
  -dhcp-iface eth1 -dhcp-pool 10.77.0.100-10.77.0.199 \
  -tftp-root ~/spike -tftp-addr 10.77.0.2:69 -boot-file bootmgfw_EX.efi \
  2>&1 | tee ~/task10-linux.log
```

Then on the host: `Start-VM nd-win`.

What to check, compared with the Windows run:

| Line | Expect |
|---|---|
| `dhcp listener started` | `iface=eth1 bind=0.0.0.0:67 server_ip=10.77.0.2` |
| `dhcp tx type=OFFER` | `siaddr=10.77.0.2` |
| TFTP sequence | Same paths as on Windows (`\BCD`, `\Boot\BCD`, policies, font, boot.sdi + boot.wim in parallel) |
| boot.wim `tftp transfer complete` | Note duration; Linux VM to VM goes through the vSwitch, not host memory |
| Result | WinPE command prompt; `ipconfig` shows a `10.77.0.x` lease with DHCP server `10.77.0.2` |

Bring the log back with `scp user@${ip}:~/task10-linux.log .tmp\logs\`.

If nothing arrives: check `DhcpGuard` is Off, that no netdeployd is still
running on the host, and that `sudo ss -ulpn | grep -E ':(67|69) '` shows
netdeployd.

## What to send back

For each task: the log file (or the lines from the table above), what
appeared on screen, and anything you changed to get it working.
