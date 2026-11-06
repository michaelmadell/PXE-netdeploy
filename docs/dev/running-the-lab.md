# Running netdeployd in the lab

Since Phase 2 step 2, network services come from the stored settings
(`GET`/`PUT /api/v1/settings`), not command-line flags. The flags that remain:

| Flag | Purpose |
|---|---|
| `-data` | Data directory: `netdeploy.db`, the generated admin certificate |
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
  `/clients` with `source: discovered`.
- A `PUT` that can't bind a port returns `422 bind_failed` and leaves the
  running configuration as it was. At startup, the same failure doesn't stop
  the server: health shows `error` with `bindFailures` so you can fix it.
- TFTP caps (`tftp.maxBlockSize` etc.) replace the old `-tftp-max-*` flags.
  Changing them keeps the socket and doesn't interrupt running transfers.
- Moving `admin` binds the new address first and closes the old one 5 s
  after the response. If you lock yourself out, start with
  `-admin-addr 127.0.0.1:8443` and fix `admin`.

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
