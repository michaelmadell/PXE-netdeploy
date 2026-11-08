# Running netdeployd in the lab

Since Phase 2 step 2, network services come from the stored settings
(`GET`/`PUT /api/v1/settings`), not command-line flags. The flags that remain:

| Flag | Purpose |
|---|---|
| `-data` | Data directory: `netdeploy.db`, the generated admin certificate |
| `-content` | Content store (imported and built files, ADR 0015); default `<data>/content`. Point it at a bigger volume if needed; it must not be shared between data directories |
| `-admin-addr` | Recovery override for the admin listener (health shows a warning while it's set) |
| `-log`, `-log-level` | Logging |
| `-agent-dir` | Where a server built without `-tags agent` finds `agent-windows-<arch>.exe` (default `dist`, after `just build`) |
| `-wimlib` | Path to `wimlib-imagex` if it isn't on PATH |

A fresh data directory starts with no interfaces, so nothing answers PXE until
you add one.

## Hyper-V lab (`NetDeployLab`, host `10.77.0.1/24`)

Status: **untested as written.** The same requests were tested on Linux
(loopback) with curl on 2026-11-06; run this once on the Windows host and fix
anything that differs.

Start the server (elevated, for UDP 67/69):

```powershell
go run .\cmd\netdeployd\
```

In another shell, add the lab interface in full DHCP mode:

```powershell
$api = 'https://127.0.0.1:8443/api/v1'
$r = Invoke-WebRequest "$api/settings" -SkipCertificateCheck
$s = $r.Content | ConvertFrom-Json
$s.interfaces = @(@{
  name     = 'vEthernet (NetDeployLab)'
  services = @{ http = $false }
  dhcp     = @{
    mode = 'full'
    pool = @{ start = '10.77.0.100'; end = '10.77.0.199'; subnetMask = '255.255.255.0' }
  }
})
$s.bootPrompt.password = 'choose-a-password'   # optional; stored as a hash
Invoke-RestMethod "$api/settings" -Method Put -SkipCertificateCheck `
  -Headers @{ 'If-Match' = $r.Headers.ETag[0] } `
  -ContentType 'application/json' -Body ($s | ConvertTo-Json -Depth 10)
```

Check `Invoke-RestMethod "$api/system/health" -SkipCertificateCheck`: `dhcp`
and `tftp` should be `ok` on `vEthernet (NetDeployLab)`. Settings persist, so
later runs only need the first command.

Notes:

- Full-mode leases are stored, so a restart keeps them. A lease the store
  can't record is not acknowledged (the client retries).
- With `pingCheck` (the default), addresses with no lease history are pinged
  before they're offered. Without raw-socket privilege the check is skipped
  with one warning in the log.
- Every PXE / HTTP Boot client that passes the filters appears in
  `GET /clients` with `source: discovered`.
- TFTP picks each client's content from its DHCP boot session
  (`boot_session=dhcp` in the log), the lease table after a restart
  (`lease`), or the default profile's boot image (`none`, e.g. with DHCP
  `off`). What a client boots comes from profiles (ADR 0024; see "Boot
  images end to end" below). Paths match case-insensitively over both
  TFTP and HTTP. `\BCD` is never served, and misses of
  the optional files Boot Manager probes for are logged at debug only.
- A `PUT` that can't bind a port returns `422 bind_failed` and leaves the
  running configuration as it was. At startup, the same failure doesn't stop
  the server: health shows `error` with `bindFailures` so you can fix it.
- TFTP caps (`tftp.maxBlockSize` etc.) replace the old `-tftp-max-*` flags.
  Changing them keeps the socket and doesn't interrupt running transfers.
- Moving `admin` binds the new address first and closes the old one 5 s
  after the response. If you lock yourself out, start with
  `-admin-addr 127.0.0.1:8443` and fix `admin`.

## Content HTTP listener (Phase 2 step 5)

Each interface with `services.http` (default port 80) gets a content listener
on its address. It serves only `/content/boot/{path}` (the requesting
client's boot content, chosen as for TFTP) and the agent API; the admin API
answers `404` there. HTTPS is planned for Phase 6 (`services.https: true` is
refused). If port 80 is taken on the host (IIS, for example), set `httpPort`.

Check from `nd-linux`:

```bash
curl -s -o /dev/null -w '%{http_code} %{size_download} %{speed_download}\n' http://10.77.0.1/content/boot/Boot/boot.wim
curl -s -o /dev/null -w '%{http_code} %{size_download}\n' -H 'Range: bytes=0-1023' http://10.77.0.1/content/boot/Boot/boot.wim
curl -s -o /dev/null -w '%{http_code}\n' http://10.77.0.1/api/v1/settings   # 404: admin API isn't here
```

Status: tested on Linux loopback (2026-11-06: 200 MB at ~1 GB/s, byte-for-byte
identical; `Range` 206; admin path 404) and across the lab switch from
`nd-linux` (2026-11-06: all three checks as expected).

## Proxy mode against real firmware (Phase 2 step 3)

Status: **tested 2026-11-06, pass** (results in
[phase-2-findings.md](../spikes/phase-2-findings.md)). Hyper-V firmware took
the boot file from the proxy offer without using port 4011.

Setup: `nd-linux` is the real DHCP server on the lab switch (dnsmasq, which
knows nothing about PXE), the Windows host runs netdeployd in proxy mode, and
`nd-client-01` boots.

1. DHCP Guard must be off for `nd-linux`, or Hyper-V drops its offers
   (elevated, on the host):

   ```powershell
   Get-VMNetworkAdapter -VMName nd-linux | Set-VMNetworkAdapter -DhcpGuard Off
   ```

   Then on `nd-linux` (`eth1` = `10.77.0.2/24`):

   ```bash
   sudo apt-get install -y dnsmasq
   sudo tee /etc/dnsmasq.d/lab.conf <<'CONF'
   port=0
   interface=eth1
   bind-interfaces
   dhcp-range=10.77.0.100,10.77.0.199,255.255.255.0,1h
   log-dhcp
   CONF
   sudo systemctl restart dnsmasq
   journalctl -u dnsmasq -f
   ```

   `port=0` turns off dnsmasq's DNS server; nothing else is PXE-specific.

2. On the Windows host, allow UDP 4011 and start netdeployd with a fresh data
   directory, so stored full-mode settings can't put a second address server
   on the switch:

   ```powershell
   New-NetFirewallRule -DisplayName 'netdeploy proxyDHCP 4011' -Direction Inbound -Protocol UDP -LocalPort 4011 -Action Allow
   go run .\cmd\netdeployd\ -data .tmp\proxy-data *>&1 |
     Tee-Object .tmp\logs\proxy-nd-client-01.log
   ```

   Then set the interface to proxy:

   ```powershell
   $s.interfaces = @(@{
     name = 'vEthernet (NetDeployLab)'; services = @{ http = $false }
     dhcp = @{ mode = 'proxy' }
   })
   ```

   and `PUT` as before. Health should show DHCP on both `:67` and `:4011`.

3. `Start-VM nd-client-01; vmconnect localhost nd-client-01` and save the netdeployd log
   to `.tmp\logs\proxy-nd-client-01.log`.

Record in the Phase 1 findings (or a Phase 2 findings file):

| Question | Where to look |
|---|---|
| Did the firmware accept the proxy offer? | netdeployd `dhcp tx type=OFFER yiaddr=-`, then TFTP requests from a `10.77.0.1xx` address |
| Did it use the offer's boot file directly, or send a REQUEST to 4011 first? | `dhcp rx type=REQUEST port=4011` in the log |
| Did netdeployd learn the client's address from its REQUEST to dnsmasq? | `noted 10.77.0.1xx for boot` |
| Does the discovered client's `smbiosUuid` match the VM's? | The client's `smbiosUuid` (`GET /clients`, once implemented; until then read `netdeploy.db` after stopping the server) vs the VM's `BIOSGUID`: `(Get-CimInstance -Namespace root\virtualization\v2 -ClassName Msvm_VirtualSystemSettingData \| ? { $_.ElementName -eq '<vm>' -and $_.VirtualSystemType -eq 'Microsoft:Hyper-V:System:Realized' }).BIOSGUID` |
| Did WinPE boot under Secure Boot? | VM console |

If the firmware ignores the offer, capture the exchange with Wireshark on the
lab switch and keep the capture with the findings before changing anything.

Afterwards, stop dnsmasq so it doesn't compete with full-mode runs:
`sudo systemctl disable --now dnsmasq`.

## Uploading and importing an ISO (Phase 3)

No lab VMs needed. Start a throwaway server (PowerShell 7):

```powershell
go run .\cmd\netdeployd -data .tmp\img-test -admin-addr 127.0.0.1:18443
```

Upload with any tus 1.0.0 client. `.tmp\tus-smoke\smoke.mjs` (git-ignored)
uses tus-js-client; `send` prints the upload URL and progress, `resume`
continues an interrupted one:

```powershell
$env:NODE_TLS_REJECT_UNAUTHORIZED = '0'   # local self-signed cert only
node .tmp\tus-smoke\smoke.mjs send C:\path\to\Win11.iso https://127.0.0.1:18443
```

Import it and watch the job (send JSON from a file: Windows PowerShell 5.1
strips the quotes from JSON passed inline to `curl.exe`):

```powershell
$b = 'https://127.0.0.1:18443/api/v1'
@{ uploadId = '<id>'; name = 'Windows 11'; retainIso = $true } | ConvertTo-Json | Set-Content body.json
$job = Invoke-RestMethod "$b/images" -Method Post -SkipCertificateCheck -ContentType application/json -InFile body.json
Invoke-RestMethod "$b/jobs/$($job.id)" -SkipCertificateCheck          # percent, message
Invoke-RestMethod "$b/images/$($job.resourceId)" -SkipCertificateCheck | ConvertTo-Json -Depth 6
```

Measured 2026-11-08 on the dev machine with a Win11 en-GB ISO (8.9 GB,
`CPBA_X64FRE_EN-GB_DV9`): upload 24 s, import 14 s, 10 editions, five boot
managers (2011- and 2023-signed as in the Phase 1 findings), 1,002 blobs.

### Driver packages

Same server and upload client. Upload a zip, 7z or CAB of driver folders
(an `.exe` or `.msi` is refused with `400 unsupported_archive`), then:

```powershell
@{ uploadId = '<id>'; name = 'Intel NICs' } | ConvertTo-Json | Set-Content body.json
$job = Invoke-RestMethod "$b/driver-packages" -Method Post -SkipCertificateCheck -ContentType application/json -InFile body.json
Invoke-RestMethod "$b/jobs/$($job.id)" -SkipCertificateCheck
(Invoke-RestMethod "$b/driver-packages/$($job.resourceId)" -SkipCertificateCheck).infs |
  Format-Table path, class, signature, enabled, signatureDetail
Invoke-RestMethod "$b/system/capabilities" -SkipCertificateCheck   # cabLzx: expand.exe found?
```

A zip of any `DriverStore\FileRepository` folder makes a good test package
(`Compress-Archive`); `makecab` with `.Set CompressionType=LZX` in a DDF makes
an LZX CAB. `.tmp\drv-e2e\run.ps1` (git-ignored) does both plus an
installer, against a throwaway server. Measured 2026-11-08: a 4.5 MB Realtek
Wi-Fi zip and an LZX CAB both import in 0.2 s as `whql`.

### Backups, secrets and restore

Store the share's credential as a secret, point `backup.destination` at the
share, and back up (ADR 0020, 0021):

```powershell
$sec = Invoke-RestMethod "$b/secrets" -Method Post -SkipCertificateCheck -ContentType application/json -InFile secret.json
#   secret.json: { "name": "NAS", "kind": "share-credential", "credential": { "username": "NAS01\\backup", "password": "..." } }
$s = Invoke-RestMethod "$b/settings" -SkipCertificateCheck
$s.backup = @{ schedule = @{ frequency = 'daily'; time = '02:00' }; retention = 5
               destination = @{ type = 'smb'; path = '\\nas01\backups\netdeploy'; credentialSecretId = $sec.id } }
Invoke-RestMethod "$b/settings" -Method Put -SkipCertificateCheck -Headers @{ 'If-Match' = $s.etag } `
  -ContentType application/json -Body ($s | ConvertTo-Json -Depth 10)
$job = Invoke-RestMethod "$b/system/backups" -Method Post -SkipCertificateCheck   # back up now (also tests the share)
Invoke-RestMethod "$b/system/backups" -SkipCertificateCheck                       # list
(Invoke-RestMethod "$b/system/health" -SkipCertificateCheck).backup              # last success/failure, next run
```

Export the secrets key once and keep it off the server; health warns
`secrets_key_not_exported` until you do:

```powershell
.\netdeployd.exe key export -data .\data -out D:\safe\netdeploy-key.json
```

Restore (server stopped; the data directory must be empty unless `-force`):

```powershell
.\netdeployd.exe restore -data .\data -from \\nas01\backups\netdeploy -user NAS01\backup -key D:\safe\netdeploy-key.json
```

`-from` may name a snapshot (`...\snapshots\20261108T020000Z`); otherwise the
newest is used. `netdeployd backup -to <folder|UNC>` makes a backup with the
server stopped. A throwaway encrypted test share: `.tmp\smbspike\setup-share.ps1`
(elevated; `cleanup-share.ps1` removes it); `.tmp\backup-e2e\run.ps1` runs the
whole cycle against it.

**Tests against real media** (not run in CI): set `NETDEPLOY_TEST_ISO` to a
Windows ISO and run

```powershell
$env:NETDEPLOY_TEST_ISO = 'C:\path\to\Win11.iso'
go test -v -run RealISO ./internal/media/... ./internal/imports
go test -v -run RealMedia ./internal/media/signer
```

The import test copies the whole ISO into a temporary content store.
`NETDEPLOY_TEST_DRIVERSTORE=1` runs the driver tests against this machine's
DriverStore (every INF parsed, every catalog verified, one package imported):

```powershell
$env:NETDEPLOY_TEST_DRIVERSTORE = '1'
go test -v -run DriverStore ./internal/drivers/... ./internal/imports
```

## Boot images (Phase 4, ADR 0022)

Builds need `wimlib-imagex` (preferred; `-wimlib` if not on PATH) or DISM
(the server elevated on Windows), and the agent: run `just build` once so
`dist\agent-windows-amd64.exe` exists for `go run`. `GET
/system/capabilities` shows the builders, `elevated` and the agent. The BCD
is written by `internal/bcd` (ADR 0023); `bcdedit` can check one we wrote
(elevated):

```powershell
$env:NETDEPLOY_TEST_BCDEDIT = '1'
go test -count=1 -v -run TestBcdedit ./internal/bcd
```

The real builder against an ISO's boot.wim (elevated for DISM; set
`NETDEPLOY_TEST_BUILDER=wimlib` and `NETDEPLOY_TEST_WIMLIB` to try wimlib):

```powershell
$env:NETDEPLOY_TEST_ISO = 'C:\path\to\Win11.iso'
go test -count=1 -v -run TestRealTools ./internal/bootimage
```

## Boot images end to end (Phase 4, ADR 0022–0024)

The server's default data directory already has the lab interface
(`vEthernet (NetDeployLab)`, full DHCP). Start it elevated, with wimlib:

```powershell
go run .\cmd\netdeployd\ -wimlib .tmp\wimlib\wimlib-imagex.exe
```

Then, in PowerShell 7 (`$b` is the admin API):

```powershell
$b = 'https://127.0.0.1:8443/api/v1'
function api($method, $path, $body, $etag) {
  $h = @{}; if ($etag) { $h['If-Match'] = $etag }
  $json = if ($body) { $body | ConvertTo-Json -Depth 8 } else { $null }
  Invoke-RestMethod "$b$path" -Method $method -SkipCertificateCheck -ContentType application/json -Body $json -Headers $h
}

# 1. Import the ISO (upload with .tmp\tus-smoke\smoke.mjs first, as above).
$job = api Post /images @{ uploadId = '<upload id>'; name = 'Windows 11' }
$imageId = (api Get "/jobs/$($job.id)").resourceId     # repeat until the job succeeds
(api Get "/images/$imageId").details.editions | Format-Table index, name

# 2. A boot image, built (auto: wimlib).
$bi = api Post /boot-images @{ name = 'WinPE x64'; sourceImageId = $imageId; architecture = 'x64'; serverUrls = @('http://10.77.0.1:8080') }
$build = api Post "/boot-images/$($bi.id)/build" @{}
api Get "/jobs/$($build.id)"                           # until succeeded
api Get "/boot-images/$($bi.id)" | Select-Object status, builtWith, sizeBytes

# 3. A profile using it, made the default.
$p = api Post /profiles @{ name = 'Win11 Pro'; bootImageId = $bi.id; installImageId = $imageId; editionIndex = 6;
                           installMethod = 'agent'; identity = @{ mode = 'none' } }
$r = Invoke-WebRequest "$b/settings" -SkipCertificateCheck
$s = $r.Content | ConvertFrom-Json
$s.bootPrompt | Add-Member -Force defaultProfileId $p.id
api Put /settings $s $r.Headers.ETag[0]
```

Boot `nd-client-01`. The DHCP log offers `bootmgfw.efi`; TFTP lines carry
`boot_image=… profile=… via="default profile"`. Without a profile to offer
the DHCP log says "no profile to offer" and the client gets an address but
no boot file.

## Deploying from the boot prompt (Phase 4, ADR 0025)

Continues from "Boot images end to end" (a built boot image, a profile made
the default). Once:

```powershell
# nd-client-01 needs a disk of at least 32 GB to install to (elevated).
New-VHD -Path "$env:PUBLIC\Documents\Hyper-V\Virtual hard disks\nd-client-01-os.vhdx" -SizeBytes 64GB -Dynamic
Add-VMHardDiskDrive -VMName nd-client-01 -Path "$env:PUBLIC\Documents\Hyper-V\Virtual hard disks\nd-client-01-os.vhdx"

# The agent talks to the content listener: turn it on at the port the boot
# image's serverUrls name (http://10.77.0.1:8080).
$r = Invoke-WebRequest "$b/settings" -SkipCertificateCheck
$s = $r.Content | ConvertFrom-Json
$s.interfaces[0].services | Add-Member -Force http $true
$s.interfaces[0].services | Add-Member -Force httpPort 8080
api Put /settings $s $r.Headers.ETag[0]
```

After changing the agent (`just agent`, then restart the server), the boot
image is `stale` (reason `agent`): build it again. Then boot `nd-client-01`:

1. The agent checks in (`agent checked in … state=locked` in the server log)
   and asks for the boot prompt password; waiting boots the local disk.
2. The right password shows the profile menu; choose, then type `yes` on
   the screen naming the disk to erase.
3. Progress: partitioning, applying-image (download, then DISM),
   injecting-drivers, configuring, finalizing; then it reports completion
   and restarts into Windows setup.

Follow it from the host:

```powershell
$d = (api Get "/deployments").items[0]; $d | Select-Object status, stage, percent, message
(api Get "/deployments/$($d.id)/logs").items | Select-Object at, level, message
```

