# Phase 6: findings

Measured behaviour and facts established while building Phase 6 (from
2026-11-18). Earlier results: [phase-5-findings.md](phase-5-findings.md).
Decisions: ADR 0028 onwards.

## Content HTTPS (decision 1, ADR 0028)

**Automated, 2026-11-18** (`go test -count=1 ./...` on the Windows lab
host, `just lint`, `just build`):

- `TestEnsureLoadAndIssue`: no CA (and no secrets key) until `Ensure`; the
  CA is RSA-3072, ten years; `content-ca-key.enc` holds no PEM; the server
  certificate's `notBefore` is the CA's and it verifies for its IP and DNS
  names (case and trailing dot normalised) and not for others; reloading
  gives the same CA.
- `TestNamesChangeReissues`: names re-read after 10 s; a new address gets a
  new certificate; renewed 30 days before expiry.
- `TestRestoreWithoutKey`: the CA files without the secrets key are
  reported lost and replaced by a new CA.
- `TestAgentTrust`: the agent's TLS config accepts the server with its
  clock at 2001-01-01, rejects an expired certificate and another CA.
- `TestContentHTTPS` (netsvc): HTTPS and HTTP listeners side by side,
  HTTP/1.1 only even when the client offers h2, the certificate from
  `GetCertificate`.
- `TestContentHTTPS` (settings): a custom pair refused; HTTPS on the HTTP
  port refused; enabling HTTPS makes the CA, starts the listener and
  reports `contentHttps.ca`/`certificate` covering the interface address and
  `advertisedAddress`.
- `TestCheck` (`mixed_server_urls`), `TestDiff` (HTTP inputs' JSON has no
  `contentCa`, so their hashes are unchanged), `TestContentCAInput`
  (refused without a CA; `contentCaPem` in `config.json`; stale
  `content-ca` when the CA changes; HTTP images unaffected).
- `TestAgentReachable`/`TestAgentOverHTTP`, `TestContentCAEndpoint` (404,
  PEM, DER), `TestContentCAWarning`.

**Lab, 2026-11-18, `nd-client-01`** (the user, Hyper-V, Secure Boot, 2023
boot manager):

| Run | Result |
|---|---|
| HTTPS on the lab interface, boot image rebuilt with `https://10.77.0.1`; armed deployment: profile `1`, Windows 11 Pro from Win 11 26H2, local accounts, the `Lab: first logon` template, `site=Lab`, computer name `LAB-1-05` | **Worked.** Ran 14:17 to 14:20, 3 minutes end to end, including the 7.6 GB `install.wim` download over TLS. No noticeable difference from the HTTP runs: TLS costs nothing visible on the lab's internal switch |
| Physical machine with an **Intel I210**, cabled to a USB Ethernet adapter that `NetDeployLab` was made an External switch on (host still `10.77.0.1`); same HTTPS boot image, **no I210 driver** in it | **Worked.** Network boot under Secure Boot, TFTP at the defaults, the agent over HTTPS, deployment finished. The 7.6 GB `install.wim` took **about 75 s**, line rate for 1 Gbit/s: slower than Hyper-V only because of the link. boot.wim over TFTP took roughly 12–15 s. Plain WinPE (boot.wim index 1) also had network. Also closes Phase 1 tasks 7–8 (WinPE's inbox driver covers the I210) |

**Not yet tested in the lab:**

| Check | How |
|---|---|
| `secrets_key_not_exported` | Appears if the key was never exported |
| Clock set back | Hyper-V's RTC follows the host, so on physical hardware: set the firmware clock before the CA's start and deploy; the agent should still connect (unit-tested only) |
| `agent_over_http` | A profile whose boot image keeps `http://10.77.0.1` while HTTPS is on |
| DER download | Settings → Admin → DER; opens as a certificate in Windows |

## Admin authentication (decision 3, ADR 0029)

**Automated, 2026-11-18** (`go test -count=1 ./...`, `just lint`,
`just build` on the Windows lab host):

- `TestAuthSetupAndSignIn`: before setup only the open operations answer
  (`401 unauthenticated` otherwise, unknown paths included, and
  `/api/v1/agent/../clients`); health without a session has statuses only;
  sign-in answers `409 setup_required`; a wrong setup code `403`, another
  site's Origin `403 bad_origin`; the right code (any case) creates the
  account with an `HttpOnly`, `Secure`, `SameSite=Strict` `__Host-` cookie and
  deletes `setup-code.txt`; a second setup `409`. Cookie changes need the
  Origin; a token (`ndt_…`) works without one, can't reach `/users` or
  `/tokens` (`403 not_for_api_tokens`), and records `lastUsedAt`. Sign-out
  clears the cookie and ends the session; 8 hours idle ends it.
- `TestSignInRateLimit`: 6 failures, then `429` with `Retry-After`, also
  from another address for that username; allowed again after the back-off;
  an unknown username answers like a wrong password.
- `TestAccounts`: duplicate usernames ignoring case, short passwords;
  changing one's own password keeps that session and ends the others; a
  password set by another account ends all of them; deleting an account
  revokes its tokens; the last account can't be deleted.
- `TestDevOrigin`: `-dev` cookie without `Secure`, Origin matching
  `X-Forwarded-Host` accepted, another origin refused.
- `TestIdempotencyKeysPerCaller`: the same key from another account isn't
  replayed. `TestPublicHealth`.
- `TestSessionsEndAtOnce` (real HTTP server, open `/events` streams):
  changing one's own password sends `session.ended` (`password_changed`) to
  that account's other session, whose stream closes, and nothing to the
  session that changed it or to another account; the admin setting the
  password ends the account's remaining session the same way.
  `TestStreamNoticesIdleSession`, `TestStillValid`: an idle session's stream
  ends with `expired` at the next heartbeat.

**Built binary, 2026-11-18** (`.tmp/auth-smoke.ps1`, a fresh data directory,
admin on 127.0.0.1:18443): setup code logged and in the file; anonymous
session `setupRequired`, `/clients` 401, health statuses only, UI 200; setup
with the code 201 and the cookie stored by curl; `/clients` with the cookie
200; a token without Origin 403 `bad_origin`, with it 201; the token on
`/users` 403 `not_for_api_tokens`, on health the full document; the request
log names `cookie:labadmin` and `token:labadmin/smoke`.

**Lab, 2026-11-18** (the user, against `C:\netdeploy\data`): these
**worked**:

| Check | How |
|---|---|
| Upgrade of the lab server | Start on `C:\netdeploy\data`: migration 0014, a setup code in the log; create the account in the UI |
| The web UI end to end | Setup page, sign-in, sign-out, Accounts page (add, set password, delete, tokens); `/events` and uploads with the cookie |
| Kicked out at once | Two browsers signed in as the same account; change the password in one: the other shows the sign-in page within a second. Same for deleting the account and for signing out in another tab |
| `netdeployd admin reset-password` | Server stopped; then sign in with the new password |

**Not yet tested:**

| Check | How |
|---|---|
| `just dev` over the LAN | `-dev` and the Vite proxy's `X-Forwarded-Host` |
| Lab scripts with a token | `running-the-lab.md` now sets `Authorization` for `Invoke-RestMethod` |

## Linux host (decision 4, ADR 0030)

**Automated, 2026-11-19:**

- Windows lab host: `go test -count=1 ./...`, `just lint`, `just build`.
  `TestWimlibSetupImage` with the real wimlib-imagex 1.14.5: a synthetic
  two-image boot.wim (boot index 2) exported at index 2, updated with
  `config.json` and `winpeshl.ini`, exported again: one bootable image, still
  the Setup image, with the added files and the 2023 boot manager.
  `TestICMPPinger` used a raw socket (unelevated shell).
- **On a Linux kernel** (the Cowork Linux VM on the lab host: Ubuntu 22.04,
  kernel 6.8, an ordinary user, no capabilities, `ping_group_range` closed):
  test binaries cross-compiled with `GOOS=linux CGO_ENABLED=0` for `hostcheck`,
  `portowner`, `dhcp`, `netsvc`, `server`, `settings`, `store`, `bootimage`,
  `contentca` all passed. The probe reported `lowPorts: false`, `icmp: none`,
  missing `CAP_NET_BIND_SERVICE` and `CAP_NET_RAW`, as expected for that
  user. `TestLookupOwnSockets` found the test's own TCP and UDP sockets
  through `/proc`; `TestLookupProc` a fake `/proc` with dnsmasq on UDP 67.
- **The Linux binary** on that VM (`scripts/linux-smoke.sh` with
  `NETDEPLOY_SMOKE_NO_TOOLS=1`: no wimtools or cabextract there): data
  directory `0700`, `setup-code.txt` `0600` and in the log, setup, token,
  `hostOs: linux`, DISM unavailable with its reason, `privileges` as above,
  health `ok` with no `data_dir_permissions`. **Passed.**

**Lab VM, 2026-11-19** (the user: `nd-linux`, Ubuntu, `netdeploy` service
account with the two capabilities, systemd unit from `running-the-lab.md`):

| Check | Result |
|---|---|
| Capabilities | **As expected**: `lowPorts: true`, ICMP raw, nothing missing |
| Port owner | **Worked**: the `422` named the process holding the port |
| `data_dir_permissions` | **Worked in the API** (the directory at `755` named with its mode and the fix). The Overview listed it only under "Things to check" at the bottom, with the status still "ok", so it looked missing. Changed: the status reads "ok, N things to check" in the warning colour and the list sits under it |
| File modes | SQLite created `netdeploy.db` `644` (and `admin-cert.pem` `644`): readable by anyone once the directory is opened. Changed: the server sets umask `077` at start, the check covers the database, the lab unit has `UMask=0077` |
| Lab setup | A stuck host adapter on `NetDeployLab` ("media disconnected" while Internal) and `eth1` unmanaged by networkd delayed the run; the service's first start failed with `203/EXEC` (systemd couldn't run the binary) |

**Not yet run:**

| Check | How |
|---|---|
| CI on Ubuntu | Push: wimtools and cabextract installed, `TestWimlibSetupImage` with the distribution's wimlib, the datagram ICMP socket (`ping_group_range` open on the runners), `scripts/linux-smoke.sh` with the tool checks |
| The lab VM end to end | `running-the-lab.md`, "A Linux host": Ubuntu 24.04 VM, service account with the two capabilities, ISO import, wimlib build of index 2, HTTPS, deploy `nd-client-01` |
| `missing_privileges` in the lab | The unit without its two capability lines |
| The fixes above | Overview's status with warnings; a fresh data directory has `netdeploy.db` `600` |
| Windows `ownerProcess` on a real conflict | E.g. HTTP 80 held by `http.sys` (expect `System (pid 4)`) |
