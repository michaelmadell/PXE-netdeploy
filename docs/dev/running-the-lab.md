# Running netdeployd in the lab

Since Phase 2 step 2, network services come from the stored settings
(`GET`/`PUT /api/v1/settings`), not command-line flags. The flags that remain:

| Flag | Purpose |
|---|---|
| `-data` | Data directory: `netdeploy.db`, the generated admin certificate |
| `-content` | Content store (imported and built files, ADR 0015); default `<data>/content`. Point it at a bigger volume if needed; it must not be shared between data directories |
| `-admin-addr` | Recovery override for the admin listener (health shows a warning while it's set) |
| `-log`, `-log-level` | Logging |
| `-tftp-root`, `-boot-file`, `-http-boot-url` | **Temporary** boot content until boot images exist (Phase 2 step 4) |

A fresh data directory starts with no interfaces, so nothing answers PXE until
you add one.

## Hyper-V lab (`NetDeployLab`, host `10.77.0.1/24`)

Status: **untested as written.** The same requests were tested on Linux
(loopback) with curl on 2026-11-06; run this once on the Windows host and fix
anything that differs.

Start the server (elevated, for UDP 67/69):

```powershell
go run .\cmd\netdeployd\ -tftp-root .tmp\spike -boot-file bootmgfw_EX.efi
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
  (`lease`), or the default (`none`, e.g. with DHCP `off`). Until boot images
  exist everything is `-tftp-root`. Paths match case-insensitively over both
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
`nd-win` boots.

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
   go run .\cmd\netdeployd\ -data .tmp\proxy-data -tftp-root .tmp\spike -boot-file bootmgfw_EX.efi *>&1 |
     Tee-Object .tmp\logs\proxy-nd-win.log
   ```

   Then set the interface to proxy:

   ```powershell
   $s.interfaces = @(@{
     name = 'vEthernet (NetDeployLab)'; services = @{ http = $false }
     dhcp = @{ mode = 'proxy' }
   })
   ```

   and `PUT` as before. Health should show DHCP on both `:67` and `:4011`.

3. `Start-VM nd-win; vmconnect localhost nd-win` and save the netdeployd log
   to `.tmp\logs\proxy-nd-win.log`.

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

**Tests against real media** (not run in CI): set `NETDEPLOY_TEST_ISO` to a
Windows ISO and run

```powershell
$env:NETDEPLOY_TEST_ISO = 'C:\path\to\Win11.iso'
go test -v -run RealISO ./internal/media/... ./internal/imports
go test -v -run RealMedia ./internal/media/signer
```

The import test copies the whole ISO into a temporary content store.
