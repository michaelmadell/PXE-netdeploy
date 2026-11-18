# 28. Content HTTPS: an internal CA, sealed, trusted only by the agent

Status: accepted (2026-11-18). Implements the "optionally HTTPS" of
[0009](0009-separate-admin-and-content-listeners.md); builds on
[0021](0021-secrets-storage.md) and [0022](0022-building-boot-images.md).

## Context
Until now the content listener spoke only HTTP. The boot prompt password
(`/agent/unlock`), the rendered unattend with its secrets (`/agent/unattend`)
and the install image all travelled in clear. Boot Manager loads the BCD and
boot.wim over TFTP and can't do HTTPS, so the content listener's HTTP clients
are the agent and HTTP Boot firmware. The agent reaches the server through
the boot image's `serverUrls`, baked into `config.json`, which already had an
empty `contentCaPem`. WinPE has no network time: it takes the clock from the
RTC, which may be years behind on a machine with a flat battery.

## Decision
- **One internal CA per installation**: RSA-3072 (firmware TLS for HTTPS
  boot often handles only RSA; to verify in decision 2), ten years,
  `CN=netdeploy content CA`. Created the first time an interface enables
  `services.https`; `/settings/content-ca` returns `404
  content_https_not_available` until then, PEM or DER afterwards.
- **Storage**: `<data>/content-ca.pem` (plain) and `<data>/content-ca-key.enc`,
  the key sealed with the secrets data key (AES-256-GCM, associated data
  `content-ca`). Both are in backups. Whoever holds the CA key can pose as the
  server to agents (see the boot prompt password and unattend secrets, serve
  a tampered image), so sealing keeps ADR 0021's rule that a backup alone
  reveals no secret. A restore without the key export can't open it: the
  server makes a new CA (logged), and HTTPS boot images show `stale`
  (`content-ca`); firmware enrolled with the old CA must be enrolled again.
  Creating the CA creates the data key if needed, so it counts toward
  `secrets_key_not_exported`.
- **Server certificate**: RSA-2048, issued from the CA, kept only in memory,
  served by `GetCertificate`. Names: every HTTPS interface's primary address
  and `advertisedAddress`, this host's name, and each host named in a boot
  image's `https://` server URLs; reissued when that set changes. Never
  issued on demand for an arbitrary SNI name. TLS 1.2 minimum.
- **Clocks**: the certificate's `notBefore` is the CA's, and the agent
  verifies with `tls.Config.Time` = the later of its clock and the CA's
  `notBefore`. A clock stuck in the past still connects; expiry is still
  checked.
- **Agent trust**: a boot image's `serverUrls` are all `http://` or all
  `https://` (`422 mixed_server_urls`), so blocking 443 can't push an agent
  onto HTTP. HTTPS images carry the CA in `config.json`; the agent trusts
  only that CA. The CA's fingerprint is a build input (`inputHash`, left out
  for HTTP images so they don't go stale) and stale reason `content-ca`.
- **Listeners**: HTTPS serves exactly what HTTP serves (`/content/boot/*`,
  `/agent/*` with ranges). `http` and `https` are independent per interface.
- **Checks** (`validateProfile`): `agent_unreachable` counts HTTPS endpoints
  for `https://` URLs; `agent_over_http` warns when an `http://` URL is
  served by an interface that also has HTTPS on. Health warns
  `content_ca_expiring` a year before the CA expires. Capabilities report
  `contentHttps`.
- **Deferred**: a custom certificate (`contentHttps.certificatePem` is
  refused with a `422 invalid_settings` field error; supporting it needs a
  trust-anchor field and name checks), CA rotation (a command when needed), cipher
  suites for firmware (decision 2).

## Consequences
- Secrets and the boot prompt password are protected once a profile's boot
  image uses `https://` URLs; existing HTTP boot images keep working.
- Changing addresses never makes boot images stale through the certificate,
  only through `serverUrls` as before.
- Losing both the machine and the key export means rebuilding HTTPS boot
  images (seconds with wimlib) and re-enrolling firmware.
- On Linux, port 443 needs `CAP_NET_BIND_SERVICE` (packaging).
- To verify in the lab: the agent over HTTPS on `nd-client-01`, the install
  image download speed over TLS against HTTP, a client with its RTC set back.
