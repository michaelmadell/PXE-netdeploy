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

**Not yet tested in the lab:**

| Check | How |
|---|---|
| `secrets_key_not_exported` | Appears if the key was never exported |
| Download and apply times separately, HTTP against HTTPS | From the deployment logs; the total above suggests the download isn't the bottleneck on Hyper-V |
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

**Not yet tested:**

| Check | How |
|---|---|
| Upgrade of the lab server | Start on `C:\netdeploy\data`: migration 0014, a setup code in the log; create the account in the UI |
| The web UI end to end | Setup page, sign-in, sign-out, Accounts page (add, set password, delete, tokens); `/events` and uploads with the cookie |
| Kicked out at once | Two browsers signed in as the same account; change the password in one: the other shows the sign-in page within a second. Same for deleting the account and for signing out in another tab |
| `just dev` over the LAN | `-dev` and the Vite proxy's `X-Forwarded-Host` |
| `netdeployd admin reset-password` | Server stopped; then sign in with the new password |
| Lab scripts with a token | `running-the-lab.md` now sets `Authorization` for `Invoke-RestMethod` |
