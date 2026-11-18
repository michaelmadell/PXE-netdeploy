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
