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
| `-wimlib` | Path to `wimlib-imagex`; default: `wimlib\wimlib-imagex.exe` next to the server (the Windows installer puts it there), else PATH |
| `-ipxe-dir` | The signed iPXE chain for HTTP(S) Boot (`<arch>/ipxe-shim.efi`, `ipxe.efi`, `wimboot`; ADR 0032). Default: `ipxe` next to the server (Windows installer), `/usr/lib/netdeploy/ipxe` (.deb), else `dist\ipxe` (`just fetch-ipxe` in a checkout) |
| `-log-file` | Write the log to a file instead of standard output (the Windows service uses `<data>\logs\netdeployd.log`; moved to `.1` past 64 MiB at start) |

Offline commands (server stopped): `key export|import`, `backup`, `restore`,
`admin reset-password`, `admin set-address -data <dir> <ip:port>` (where the
admin web UI listens), and on Windows `service install|uninstall|start|stop`
(ADR 0031).

A fresh data directory starts with no interfaces, so nothing answers PXE until
you add one.

## Setting up a lab host from scratch

Written 2026-11-11 for a second Windows PC; the first lab host was set up
piecemeal during Phases 1–4. Untested as a whole: note anything that differs.

**Prerequisites:** Windows 11 with Hyper-V and its PowerShell module, Go
1.27.3, `just`, PowerShell 7, Node with pnpm, git. `just setup` checks them,
installs the web dependencies and generates code. wimlib: unzip the Windows
release (`wimlib-1.14.x-windows-x86_64-bin.zip`) into `.tmp\wimlib`
(git-ignored); `just dev` passes it to the server.

**Disk space:** the content store keeps the uploaded ISO and the imported
image (about 20 GB for one Win11 ISO) plus about 1 GB per boot image. If the
repo's volume is short of space, put the data directory elsewhere:
`just dev -DataDir C:\netdeploy\data` (or `-content` on the server).

1. **Lab switch and VM** (elevated PowerShell 7, repo root):

   ```powershell
   just lab
   ```

   Creates the internal switch `NetDeployLab` with the host at
   `10.77.0.1/24`, firewall rules on that adapter only (UDP 67/69/4011, TCP
   80/443; add one if the content listener uses another port), and
   `nd-client-01`: Gen 2, Secure Boot (`MicrosoftWindows`), vTPM, 4 GB
   static memory, an 80 GB disk, network boot first. Check that nothing else
   holds the ports: `Get-NetUDPEndpoint -LocalPort 67, 69` should be empty
   (the Default Switch's ICS on its own address is fine).

2. **Run the server and UI** (elevated PowerShell 7, for UDP 67/69):

   ```powershell
   just dev -DataDir C:\netdeploy\data
   ```

   Builds the agents and the server, then serves the UI at
   http://localhost:5173 (admin API on https://127.0.0.1:8443).

3. **Configure in the UI:**
   - Settings: add `vEthernet (NetDeployLab)`, DHCP `full` with pool
     `10.77.0.100`–`10.77.0.199` (`255.255.255.0`), TFTP on, HTTP on (port 80);
     set a boot prompt password. Save.
   - Images: upload the Win11 ISO; it imports when the upload finishes.
   - Boot images: create one from that image (x64, server address
     `http://10.77.0.1`, as suggested) and build it.
   - Profiles: create one with an edition, the boot image, and make it the
     default.
   - Overview: the "Ready to network boot?" checklist should be all green.

   The same steps through the API are in "Boot images end to end" and
   "Deploying from the boot prompt" below.

4. **Boot:** `Start-VM nd-client-01; vmconnect localhost nd-client-01`, then
   as in "Deploying from the boot prompt".

## Hyper-V lab (`NetDeployLab`, host `10.77.0.1/24`)

Status: **untested as written.** The same requests were tested on Linux
(loopback) with curl on 2026-11-06; run this once on the Windows host and fix
anything that differs.

Start the server (elevated, for UDP 67/69):

```powershell
go run .\cmd\netdeployd\
```

**Signing in (ADR 0029, from 2026-11-18).** Every admin call needs a session
or an API token. On the first start the server logs a setup code (also in
`<data>/setup-code.txt`); open the web UI and create the first account with
it. For the commands below, create a token on the Accounts page and set
`$PSDefaultParameterValues` once per shell so every call sends it:

```powershell
$env:ND_TOKEN = 'ndt_…'   # from Accounts → Create token
$PSDefaultParameterValues['Invoke-RestMethod:Headers'] = @{ Authorization = "Bearer $env:ND_TOKEN" }
$PSDefaultParameterValues['Invoke-WebRequest:Headers'] = @{ Authorization = "Bearer $env:ND_TOKEN" }
```

Calls that pass their own `-Headers` (the `If-Match` ones) need the
`Authorization` entry added to that table too. Locked out: stop the server
and run `netdeployd admin reset-password -data <dir> -user <name>`.

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
answers `404` there. `services.https` adds the same over TLS (ADR 0028). If
port 80 is taken on the host (IIS, for example), set `httpPort`; the `422`
names the process holding it (ADR 0030).

Check from `nd-linux`:

```bash
curl -s -o /dev/null -w '%{http_code} %{size_download} %{speed_download}\n' http://10.77.0.1/content/boot/Boot/boot.wim
curl -s -o /dev/null -w '%{http_code} %{size_download}\n' -H 'Range: bytes=0-1023' http://10.77.0.1/content/boot/Boot/boot.wim
curl -s -o /dev/null -w '%{http_code}\n' http://10.77.0.1/api/v1/settings   # 404: admin API isn't here
```

Status: tested on Linux loopback (2026-11-06: 200 MB at ~1 GB/s, byte-for-byte
identical; `Range` 206; admin path 404) and across the lab switch from
`nd-linux` (2026-11-06: all three checks as expected).

## HTTP(S) Boot (Phase 6, ADR 0032)

HTTP Boot clients get Microsoft-signed shim → iPXE-signed `ipxe.efi` →
a generated `autoexec.ipxe` → Microsoft-signed `wimboot` → the same
`bootmgfw.efi`, BCD and boot.wim a PXE client gets. The files are served
from `/content/boot/ipxe/<arch>/` on every content listener.

In a checkout, run `just fetch-ipxe` once: it downloads the chain into
`dist\ipxe` and checks it against pinned SHA-256s. The packages ship it.
`GET /system/capabilities` → `httpBoot` lists what was loaded. With
`architectures.httpBoot` on and the chain missing, health warns
`http_boot_unavailable`.

The firmware needs:

- Secure Boot trusting Microsoft's **third-party** UEFI CA. The 2023 one is
  enough: the shim and wimboot carry both signatures. BIOS options are
  named "Microsoft UEFI CA 2023", "Enable MS UEFI CA Key", or similar.
- For HTTPS, the content CA enrolled: Settings → Content HTTPS → "DER (for firmware)",
  then import it in the BIOS's HTTPS Boot certificate setting. iPXE
  reads the same certificate from the firmware, so nothing else is needed.

With our DHCP (full or proxy) on the interface, nothing needs typing in.
The answer is `https://<interface address>[:httpsPort]/content/boot/ipxe/x64/ipxe-shim.efi`
when the interface serves HTTPS, else the `http://` URL. Without our DHCP,
type that URL into the firmware's HTTP Boot setting. The client then gets
the default profile's boot image.

Watch the log: `content served` lines for `ipxe/x64/ipxe-shim.efi`, then
`ipxe.efi` and `autoexec.ipxe`, then the boot image's files with
`ua` iPXE. The shim also asks for `revocations_sku.efi`,
`revocations_sbat.efi` and `shim_certificate_0.efi`; the 404s for those are
expected.

Status: on the Intel NUC with Secure Boot on (spike, 2026-11-20), the chain
booted WinPE over HTTPS with both the iPXE v2.0.0 shim and the dual-signed
ipxe-16.1 shim. The first ipxe-16.1 boot once ended in a firmware "Secure
Boot Violation" that didn't recur; if you see one, boot again. The server's
own answer still needs a lab run.

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
api Get "/boot-images/$($bi.id)" | Select-Object status, builtWith, sizeBytes, sourceIndex, canAddOsDrivers

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
# Only for a VM without a disk of at least 32 GB (`just lab` VMs have 80 GB; elevated).
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


## Arming deployments, identity, cancelling (Phase 4, ADR 0026)

Continues from "Deploying from the boot prompt". After `just dev` rebuilds
the agent, build the boot image again (it is out of date: reason `agent`);
arming refuses a profile whose boot image isn't built and current.

```powershell
$b = 'https://127.0.0.1:8443/api/v1'
function api($method, $path, $body, $etag) {
  $h = @{}; if ($etag) { $h['If-Match'] = $etag }
  $json = if ($body) { $body | ConvertTo-Json -Depth 8 } else { $null }
  Invoke-RestMethod "$b$path" -Method $method -SkipCertificateCheck -ContentType application/json -Body $json -Headers $h
}
$client = (api Get '/clients').items | ? mac -eq '00:15:5d:xx:xx:xx'   # nd-client-01's MAC (Get-VMNetworkAdapter)
$p = (api Get '/profiles').items[0]
```

**Local accounts, computer name, unattended OOBE.** A password secret, the
profile switched to `local` with a name pattern, then validated:

```powershell
$pw = api Post /secrets @{ name = 'Lab admin'; kind = 'password'; value = 'Choose-A-Password1' }
$p.identity = @{ mode = 'local'; accounts = @(@{ name = 'labadmin'; group = 'Administrators'; passwordSecretId = $pw.id })
                 autoLogon = @{ account = 'labadmin'; count = 1 }; hideOobe = $true }
$p | Add-Member -Force osSettings @{ computerNamePattern = 'LAB-{{ .Client.Serial | last 4 }}' }
function save-profile { $id = $p.id; $etag = $p.etag
  'id','etag','createdAt','updatedAt' | % { $p.PSObject.Properties.Remove($_) }
  $script:p = api Put "/profiles/$id" $p $etag }
save-profile
api Post "/profiles/$($p.id)/validate"          # ok: true; findings explain anything else
```

Names must be 1–15 letters, digits or hyphens; the serial comes from the
agent's check-in (Hyper-V's look like `…-1234-56`). Arming shows the
rendered `computerName`, or refuses with `invalid_computer_name`. Run
`save-profile` after each change to `$p` below.

**Arming:** the client runs it on its next network boot with no password,
after a 10 s countdown:

```powershell
$d = api Post /deployments @{ clientId = $client.id; profileId = $p.id }
$d | Select-Object status, computerName                      # pending, the rendered name
Start-VM nd-client-01
```

Expect: no password prompt; the deployment runs; Windows Setup finishes
without OOBE questions and signs `labadmin` in once; the computer name is
the one above. `C:\Windows\Panther\unattend.xml` is gone after Setup.
Arming a client that already has a pending or running deployment is `409`;
an armed deployment not picked up within 72 h fails `expired`; a running one
the agent goes quiet on for 30 min fails `timeout`
(`Settings.deployments`).

**Cancelling:** arm, boot, and during "applying-image" cancel it:

```powershell
api Post "/deployments/$($d.id)/cancel"
```

Expect within a minute: the agent's screen says the deployment was cancelled
on the server and that it is stopping once the current command finishes;
DISM finishes applying; nothing after it runs; the logs end with "deployment
stopped: cancelled on the server"; the VM restarts after 2 minutes (or a
key). The status stays `cancelled`.

**AD join, start to finish.** Written 2026-11-16 after a first credentials
join failed: the lab DHCP handed out no DNS server, so Setup couldn't find
the domain, and every machine was named `LAB-` (the client had no serial on
record, and a trailing hyphen isn't a valid host name). Both are now caught:
validation warns `dhcp_no_dns`, and a name pattern that uses an empty field
or ends in a hyphen is refused.

1. *The domain controller on the lab switch.* Setup's specialize pass must
   reach a DC and resolve the domain. With the DC as a Hyper-V VM on this
   host, give it a second adapter on `NetDeployLab` (elevated):

   ```powershell
   Add-VMNetworkAdapter -VMName '<DC VM>' -SwitchName NetDeployLab -Name lab
   ```

   In the DC, give that adapter a static `10.77.0.10/24` with no gateway and
   no DNS change, then `ipconfig /registerdns`. Check from the host:

   ```powershell
   Test-NetConnection 10.77.0.10 -Port 389
   Resolve-DnsName michael-ad.test -Server 10.77.0.10
   ```

2. *DHCP hands out the DC as DNS server* (one change to the lab interface):

   ```powershell
   $r = Invoke-WebRequest "$b/settings" -SkipCertificateCheck; $s = $r.Content | ConvertFrom-Json
   $s.interfaces[0].dhcp.pool.dnsServers = @('10.77.0.10')
   $s.interfaces[0].dhcp.pool.domainName = 'michael-ad.test'
   api Put /settings $s $r.Headers.ETag[0]
   ```

3. *A join account and the OU.* The OU in the profile must exist
   (`OU=Lab,DC=michael-ad,DC=test`), and the account needs rights to create
   computer objects in it (in a lab, a Domain Admin will do; otherwise
   delegate "Create Computer objects" on the OU).

4. *The credential secret.* Values can't be read back, so if in doubt make
   a new one. `GET /secrets/{id}` shows the username as `hint`.
   The username can be `user@dns.domain` (preferred: it names the domain
   by its DNS name, as the join does) or `NETBIOS\user`; the profile's
   `domain` is always the DNS name.

   ```powershell
   $cred = api Post /secrets @{ name = 'Lab join'; kind = 'domain-credential'
                               credential = @{ username = 'svc-join@michael-ad.test'; password = '<password>' } }
   ```

5. *The profile*: AD credentials join, a break-glass local administrator so
   you can sign in and read the logs if the join fails, and a name pattern
   (the serial is recorded at the agent's check-in):

   ```powershell
   $pw = api Post /secrets @{ name = 'Lab breakglass'; kind = 'password'; value = '<password>' }
   $p = (api Get '/profiles').items[0]
   $p.identity = @{ mode = 'ad'; joinMethod = 'credentials'; domain = 'michael-ad.test'
                    organizationalUnit = 'OU=Lab,DC=michael-ad,DC=test'; joinCredentialSecretId = $cred.id
                    localAdmin = @{ name = 'breakglass'; passwordSecretId = $pw.id } }
   $p.osSettings.computerNamePattern = 'LAB-{{ .Client.Serial | last 4 }}'
   save-profile
   api Post "/profiles/$($p.id)/validate"   # ok: true, and no dhcp_no_dns warning
   ```

6. *Deploy from the boot prompt* (simplest: the check-in records the serial
   before the name is rendered). To arm from the API instead, boot the VM to
   the prompt once first, check `(api Get "/clients/$($client.id)").serial`
   isn't empty, then `api Post /deployments @{ clientId = $client.id; profileId = $p.id }`.
   The deployment's `computerName` must not be empty or end in `-`.

7. *Expect*: Setup restarts once more than with `local` (the join happens in
   specialize), then a sign-in screen that offers "Other user"; sign in as
   `MICHAEL-DOM\<user>` (the domain's NetBIOS name). The computer object is in `OU=Lab`.

8. *If it didn't join*, sign in as `.\breakglass` and read
   `C:\Windows\debug\NetSetup.LOG` (the join attempt and its error code) and
   `C:\Windows\Panther\UnattendGC\setupact.log` (search `UnattendedJoin`).
   Common codes: `1355` the domain can't be found (DNS), `5` access denied
   (the account's rights on the OU), `1326` wrong username or password,
   `2224` the computer account exists and the account can't reuse it.
   The unattend file itself is deleted after Setup.

**AD, offline join (ODJ).** No DC needed during Setup; signing in with a
domain account later needs one. On a domain-joined machine with rights to
create the computer account:

```powershell
djoin /provision /domain michael-ad.test /machine LAB-01 /machineou "OU=Lab,DC=michael-ad,DC=test" /savefile C:\temp\LAB-01.odj
```

Upload the file (tus, one request for a small file), store it as an
`odj-blob` secret and attach it to the client:

```powershell
$bytes = [IO.File]::ReadAllBytes('C:\temp\LAB-01.odj')
$r = Invoke-WebRequest "$b/uploads" -Method Post -SkipCertificateCheck -Headers @{ 'Tus-Resumable' = '1.0.0'; 'Upload-Length' = $bytes.Length }
$up = $r.Headers.Location[0]; if ($up -notmatch '^https') { $up = "https://127.0.0.1:8443$up" }
Invoke-WebRequest $up -Method Patch -SkipCertificateCheck -Body $bytes -ContentType 'application/offset+octet-stream' `
  -Headers @{ 'Tus-Resumable' = '1.0.0'; 'Upload-Offset' = '0' } | Out-Null
$blob = api Post /secrets @{ name = 'LAB-01 ODJ'; kind = 'odj-blob'; uploadId = ($up -split '/')[-1] }
$cj = api Get "/clients/$($client.id)"; $etag = $cj.etag   # the ETag is in the body, not a header
'id','etag','createdAt','updatedAt','lastSeenAt','lastIp','lastArch','source' | % { $cj.PSObject.Properties.Remove($_) }
$cj | Add-Member -Force secretRefs @{ odjBlob = $blob.id }
api Put "/clients/$($client.id)" $cj $etag
$p.identity = @{ mode = 'ad'; joinMethod = 'odj'; localAdmin = @{ name = 'breakglass'; passwordSecretId = $pw.id } }
save-profile
```

`POST /secrets` refuses a file that isn't a `djoin /provision` blob. The
blob goes into the unattend file and Setup joins offline in specialize
(WinPE has no `djoin.exe`). It names the computer (`LAB-01`): the unattend
has no `ComputerName` and the profile's name pattern is ignored. Then
deploy as before (boot prompt, or arm the client) and expect, as with the
credentials join, a sign-in screen that takes `MICHAEL-DOM\<user>`; the
computer object already exists from `/provision`. If it didn't join,
`Windows\debug\NetSetup.LOG` and `Windows\Panther\UnattendGC\setupact.log`
(search `Provisioning`, `ODJ`) have the reason. Safest is one blob per
install: provision a new one (`/reuse` keeps the name and account) for each
redeploy.

## Unattend templates (Phase 5, ADR 0027)

Continues with `api`, `$p`, `$client` and `save-profile` from "Arming
deployments". A copy of the built-in template with a first-logon command
that writes a variable to a file, so it shows the template took effect.
Use the `local` profile with auto-logon from above (the command needs a
sign-in).

```powershell
$bi = api Get /unattend-templates/builtin
$fl = @'
      <FirstLogonCommands>
        <SynchronousCommand wcm:action="add">
          <Order>1</Order>
          <CommandLine>cmd /c echo site {{ .Variables.site }} deployment {{ .Deployment.ID }} &gt; C:\netdeploy-template.txt</CommandLine>
          <Description>netdeploy template test</Description>
        </SynchronousCommand>
      </FirstLogonCommands>

'@ -replace "`r`n", "`n"
$end = "    </component>`n  </settings>`n</unattend>"
$t = api Post /unattend-templates @{ name = 'Lab: first logon'; content = $bi.content.Replace($end, $fl + $end)
                                     identityModes = @('none', 'local', 'ad'); requiredVariables = @('site') }
(api Post "/unattend-templates/$($t.id)/render" @{ identityMode = 'local' }).content   # sample data, secrets ********
$p.unattendTemplateId = $t.id; save-profile
api Post "/profiles/$($p.id)/validate"          # warns missing_variable for clients without `site`
```

Literal `>` in the template's own XML is written `&gt;`; values from
`{{ … }}` are escaped automatically. Saving refuses a template that doesn't
parse (`template_syntax`), uses a variable it doesn't declare
(`template_render`) or drops what an identity mode needs, such as the
accounts or the domain join (`template_invalid`, one entry per problem).

Give the client the variable, preview what it will get, arm and boot:

```powershell
$cj = api Get "/clients/$($client.id)"; $etag = $cj.etag
'id','etag','createdAt','updatedAt','lastSeenAt','lastIp','lastArch','source' | % { $cj.PSObject.Properties.Remove($_) }
$cj | Add-Member -Force variables @{ site = 'lab' }
api Put "/clients/$($client.id)" $cj $etag
(api Post "/unattend-templates/$($t.id)/render" @{ profileId = $p.id; clientId = $client.id }) | Select-Object ok, findings
$d = api Post /deployments @{ clientId = $client.id; profileId = $p.id }
$d.unattendTemplate; $d.variables                # the copy the deployment keeps
Start-VM nd-client-01
```

Expect: after Setup signs `labadmin` in, `C:\netdeploy-template.txt` says
`site lab deployment <id>`. Editing the template after arming doesn't
change this deployment; deleting a template a profile uses is `409
template_in_use`, and the built-in one is read-only (`409 built_in`).

## OS drivers and the Setup image (Phase 5, ADR 0022 amendment)

The agent can add OS drivers only from boot.wim's Windows Setup image
(index 2 on install media); plain WinPE (index 1) fails DISM with
`0x80004002`. A new boot image without `sourceIndex` uses the Setup image.
Check the refusal with a plain WinPE boot image (continues with `api`,
`$imageId` and a profile `$p` that has `osDriverGroupIds`):

```powershell
$pe = api Post /boot-images @{ name = 'WinPE plain'; sourceImageId = $imageId; sourceIndex = 1; architecture = 'x64';
                               serverUrls = @('http://10.77.0.1:8080') }
$build = api Post "/boot-images/$($pe.id)/build" @{}
api Get "/jobs/$($build.id)"                              # until succeeded
api Get "/boot-images/$($pe.id)" | Select-Object canAddOsDrivers, warnings   # False, the plain-WinPE warning
$p.bootImageId = $pe.id; save-profile
(api Post "/profiles/$($p.id)/validate").findings          # error boot_image_cannot_add_drivers
api Post /deployments @{ clientId = $client.id; profileId = $p.id }   # 422 profile_invalid
```

Put the profile back on the Setup boot image (`canAddOsDrivers: true`) and
deploy: the log shows `injecting-drivers` and the drivers' count. Boot
images built before this change show `stale` (`recipe`) and
`canAddOsDrivers` empty until rebuilt; changing `sourceIndex` from 1 to 2 marks it stale
(`source-index`).


## Installing from the packages (Phase 6, ADR 0031)

`just package` builds into `dist`: `netdeploy-<version>-setup.exe` (needs
Inno Setup: `winget install JRSoftware.InnoSetup`, and wimlib unzipped in
`.tmp\wimlib`) and `netdeploy_<version>_amd64.deb` / `_arm64.deb`. Until
the first `v*` tag the version is `0.0.0-<commits>-g<hash>`.

**Windows** (on a host that isn't already running netdeployd from `just dev`,
or stop that first: both want the same ports):

1. Run the installer (it asks for elevation; SmartScreen warns: unsigned).
   Pages: the data folder (default `C:\ProgramData\netdeploy`), where the
   admin web UI listens (this computer only, or all networks) and its port,
   and the firewall rule.
2. The finish page shows the setup code and opens `https://localhost:<port>`.
3. Check: `Get-Service netdeploy` (Running, Automatic);
   `Get-Content C:\ProgramData\netdeploy\logs\netdeployd.log -Tail 20`;
   `(Get-Acl C:\ProgramData\netdeploy).Access` lists SYSTEM, Administrators
   and you, not inherited; capabilities show wimlib from
   `C:\Program Files\netdeploy\wimlib`, and DISM available (LocalSystem).
4. Upgrade: run a newer installer; it stops the service, replaces the files
   and starts it, without asking the questions again. The database is
   snapshotted before any migration (`netdeploy.db.bak-v<N>-<time>`).
5. Uninstall (Settings → Apps): removes the service and firewall rule, then
   asks whether to remove the data; Yes moves it to
   `netdeploy.removed-<time>` next to it.

**Ubuntu** (24.04; replaces steps 3–4 of "A Linux host" below):

```bash
sudo apt install ./netdeploy_<version>_amd64.deb     # asks where the admin UI listens; prints the setup code
systemctl status netdeploy
sudo dpkg-reconfigure netdeploy                        # change the admin address later
sudo apt purge netdeploy                               # data moved to /var/lib/netdeploy.purged-<time>
```

Extra flags (a content store elsewhere, for example) go in
`/etc/default/netdeploy`; the file explains the drop-in a content store
outside `/var/lib/netdeploy` needs. CI runs the whole cycle (install,
reconfigure, purge, reinstall) with `scripts/deb-smoke.sh`.

## A Linux host (Phase 6, ADR 0030)

Runs the whole server in an Ubuntu VM on `NetDeployLab` as a non-root service
account with two capabilities, and deploys `nd-client-01` from it. The VM is
a second netdeploy server with its own data: images, boot images and
profiles are made again there.

**Only one DHCP server on the lab switch.** Stop the Windows host's
netdeployd (or set its interface's DHCP to `off`) for as long as the Linux
server serves `NetDeployLab`, and start it again afterwards.

### 1. The VM (Hyper-V host, elevated PowerShell)

If `nd-linux` from Phases 1–2 still exists, reuse it: skip to step 3, but
first `sudo systemctl disable --now dnsmasq` there (the proxy-mode test's
DHCP server) and check `eth1` is still `10.77.0.2/24`.

Otherwise download Ubuntu Server 24.04 LTS (`ubuntu-24.04.x-live-server-amd64.iso`),
then:

```powershell
$iso = 'D:\ISOs\ubuntu-24.04.3-live-server-amd64.iso'   # your path
New-VM -Name nd-linux -Generation 2 -MemoryStartupBytes 4GB -SwitchName 'Default Switch' `
  -NewVHDPath 'C:\Hyper-V\nd-linux.vhdx' -NewVHDSizeBytes 80GB
Set-VMProcessor nd-linux -Count 2
Set-VMFirmware nd-linux -SecureBootTemplate MicrosoftUEFICertificateAuthority   # Linux needs the third-party CA
Add-VMNetworkAdapter -VMName nd-linux -SwitchName NetDeployLab -Name lab
Add-VMDvdDrive -VMName nd-linux -Path $iso
Set-VMFirmware nd-linux -FirstBootDevice (Get-VMDvdDrive nd-linux)
Get-VMNetworkAdapter nd-linux | Format-Table Name, SwitchName, MacAddress, DhcpGuard
Start-VM nd-linux; vmconnect localhost nd-linux
```

80 GB leaves room for one Windows ISO (~9 GB), its extracted content and
boot images. `DhcpGuard` must stay `Off` on the `lab` adapter (the default),
or the VM's DHCP offers are dropped.

In the installer: the default (Default Switch) adapter on DHCP, the
`NetDeployLab` adapter left unconfigured (it gets an address below), **Install
OpenSSH server** ticked, and a user of your choice (`labuser` below). After
the reboot, remove the ISO: `Get-VMDvdDrive nd-linux | Set-VMDvdDrive -Path $null`.

### 2. The lab address (in the VM)

Find the lab adapter by its MAC from step 1 (Hyper-V shows `00155D...`, Linux
`00:15:5d:...`):

```bash
ip -br link            # e.g. eth0 (Default Switch), eth1 (NetDeployLab)
ip -br -4 addr show eth0   # the address to reach the VM from the host
sudo tee /etc/netplan/60-netdeploy-lab.yaml >/dev/null <<'YAML'
network:
  version: 2
  ethernets:
    eth1:
      dhcp4: false
      addresses: [10.77.0.2/24]
YAML
sudo chmod 600 /etc/netplan/60-netdeploy-lab.yaml
sudo netplan apply
ping -c1 10.77.0.1     # the Windows host on the lab switch
```

Replace `eth1` with the name `ip -br link` showed.

### 3. Packages and the binary

Since ADR 0031: install the `.deb` instead of steps 3 and 4 (see
"Installing from the packages" above). By hand:

```bash
sudo apt update && sudo apt install -y wimtools cabextract
wimlib-imagex --version | head -1
```

On the Windows host, build and copy the Linux binary (`just build` writes
`dist\netdeployd-linux-amd64`, with the UI and agents inside):

```powershell
scp dist\netdeployd-linux-amd64 labuser@<VM eth0 address>:netdeployd
```

### 4. The service account and unit (in the VM)

The `.deb` (ADR 0031) does all of this; by hand:

```bash
sudo useradd --system --home-dir /var/lib/netdeploy --shell /usr/sbin/nologin netdeploy
sudo install -d -o netdeploy -g netdeploy -m 700 /var/lib/netdeploy
sudo install -m 755 ~/netdeployd /usr/local/bin/netdeployd
sudo tee /etc/systemd/system/netdeploy.service >/dev/null <<'UNIT'
[Unit]
Description=netdeploy (lab)
After=network-online.target
Wants=network-online.target

[Service]
User=netdeploy
Group=netdeploy
# Admin UI on every address of the VM: lab only. Normally a management address.
ExecStart=/usr/local/bin/netdeployd -data /var/lib/netdeploy -admin-addr 0.0.0.0:8443
AmbientCapabilities=CAP_NET_BIND_SERVICE CAP_NET_RAW
CapabilityBoundingSet=CAP_NET_BIND_SERVICE CAP_NET_RAW
NoNewPrivileges=yes
UMask=0077
Restart=on-failure

[Install]
WantedBy=multi-user.target
UNIT
sudo systemctl daemon-reload
sudo systemctl enable --now netdeploy
journalctl -u netdeploy -n 30 --no-pager | grep -E 'setup_code|error'
```

The server sets its own umask to `077` (the unit's `UMask=` is a second
line of defence), so everything it creates is private. A data directory
from a build before 2026-11-19 has a world-readable `netdeploy.db`: health
warns `data_dir_permissions` until you `sudo chmod 600
/var/lib/netdeploy/netdeploy.db*`.

The log line `no admin account yet ... setup_code=XXXXX-XXXXX` has the code
(also in `/var/lib/netdeploy/setup-code.txt`, readable only by `netdeploy`:
`sudo cat` it).

Ubuntu's `ufw` is off by default. If it's on:
`sudo ufw allow in on eth1 proto udp to any port 67,69,4011`,
`sudo ufw allow in on eth1 proto tcp to any port 80,443` and
`sudo ufw allow in on eth0 proto tcp to any port 8443`.

### 5. Set it up and deploy (from the Windows host's browser)

Stop the Windows host's netdeployd first (see the top of this section).

1. Open `https://<VM eth0 address>:8443`, accept the self-signed
   certificate, create the account with the setup code.
2. **Settings → Network:** add interface `eth1`, DHCP **full** with pool
   `10.77.0.100`–`10.77.0.199`, mask `255.255.255.0`, ping check on; TFTP,
   HTTP and **HTTPS** on. Save. Overview's health should have **no**
   `missing_privileges`, `dhcp_ping_unavailable` or `data_dir_permissions`
   warnings; `GET /api/v1/system/capabilities` shows
   `"privileges":{"icmp":"raw","lowPorts":true,"missing":[]}` and wimlib
   available.
3. **Images:** upload `Win 11 26H2.iso`; wait for the import.
4. **Boot images:** new, from that image (index 2 by default), server address
   `https://10.77.0.2` (suggested); build. This is the check that the
   distribution's wimlib exports and updates the Setup image like the Windows
   build (`canAddOsDrivers: true`).
5. **Profiles:** one with that boot image, Windows 11 Pro; make it the default.
6. Boot `nd-client-01` from the network (arm a deployment, or use the boot
   prompt). It should deploy as from the Windows host.

### 6. Linux-specific checks

| Check | How | Expect |
|---|---|---|
| Port owner | `sudo -u netdeploy python3 -m http.server 8080 --bind 10.77.0.2 &`, then set the interface's HTTP port to 8080 and save | `422` naming `python3 (pid N)`; stop it with `kill %1` |
| Data directory | `sudo chmod 755 /var/lib/netdeploy`; Overview within 5 s | Status "ok, N things to check" and `data_dir_permissions` at the top; `sudo chmod 700 /var/lib/netdeploy` clears it |
| No capabilities | Comment out the two capability lines, `sudo systemctl daemon-reload && sudo systemctl restart netdeploy` | `missing_privileges` naming DHCP 67, TFTP 69, HTTP 80, HTTPS 443; `dhcp_ping_unavailable` unless the datagram socket is allowed (`sysctl net.ipv4.ping_group_range`). Put the lines back afterwards |

When finished: `sudo systemctl stop netdeploy` in the VM (or shut it down),
then start the Windows host's netdeployd again.
