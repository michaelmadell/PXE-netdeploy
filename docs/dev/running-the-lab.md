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

- `proxy` mode returns `422` until Phase 2 step 3.
- A `PUT` that can't bind a port returns `422 bind_failed` and leaves the
  running configuration as it was. At startup, the same failure doesn't stop
  the server: health shows `error` with `bindFailures` so you can fix it.
- TFTP caps (`tftp.maxBlockSize` etc.) replace the old `-tftp-max-*` flags.
  Changing them keeps the socket and doesn't interrupt running transfers.
- Moving `admin` binds the new address first and closes the old one 5 s
  after the response. If you lock yourself out, start with
  `-admin-addr 127.0.0.1:8443` and fix `admin`.
