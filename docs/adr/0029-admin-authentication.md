# 29. Admin authentication: local accounts, session cookies, API tokens

Status: accepted (2026-11-18). Builds on [0009](0009-separate-admin-and-content-listeners.md);
the agent's own tokens ([0025](0025-agent.md)) are unchanged.

## Context
The spec declared `bearerAuth` on every admin operation from Phase 0, but
nothing enforced it: anyone who could reach the admin listener (localhost by
default) had full control, including arming deployments that wipe disks. The
web UI uses `EventSource` for `/events`, which can't send an `Authorization`
header, and tus uploads and downloads go through plain `fetch`/XHR. The CLI
commands take the data lock, so they only run while the server is stopped,
and a server running as a service shouldn't need stopping to be set up.

## Decision
- **Two kinds of caller.** People sign in to the web UI with a username and
  password and get a session cookie; automation sends an API token as
  `Authorization: Bearer`. Either works on every admin operation. A token kept
  in browser storage is easier to steal than an `HttpOnly` cookie, and the
  cookie also covers `EventSource`, uploads and downloads unchanged.
- **Named local accounts, all full admins.** No roles yet (read-only is the
  natural next one). Usernames `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`, unique
  ignoring case; passwords 8 to 256 characters (the boot prompt's minimum),
  argon2id with the boot prompt's parameters. The last account can't be deleted (`409 last_user`).
  Deleting an account ends its sessions and revokes the tokens it created;
  changing a password (own or another's) ends that account's other sessions.
- **Sessions** in SQLite, the ID (32 random bytes) stored as its SHA-256.
  Cookie `__Host-netdeploy-session`: `HttpOnly`, `Secure`, `SameSite=Strict`,
  `Path=/`. A session ends after 8 hours idle and 7 days after sign-in;
  activity is recorded at most once a minute. Sign-out deletes it.
- **CSRF.** Besides `SameSite=Strict`, cookie requests with `POST`, `PUT`,
  `PATCH` or `DELETE` must send an `Origin` whose host is the request's
  `Host` (`403 bad_origin`). Bearer requests skip the check.
- **API tokens**: `ndt_` + 32 random bytes (base64url), stored as SHA-256,
  shown once, optional expiry, `lastUsedAt` at most once a minute,
  `createdBy`. Created only with a session; tokens can't call `/users`,
  `/tokens` or change a password (`403 not_for_api_tokens`), so a leaked
  token can't mint more access. `POST /tokens` ignores `Idempotency-Key`,
  whose stored replay would keep the secret.
- **What is open.** UI files, `GET /auth/session`, `POST /auth/sign-in`,
  `POST /auth/setup`, `GET /system/health` (status only: overall and per
  service, no detail, warnings, storage or backup) and the agent API (its
  own tokens). Everything else under `/api/v1/` is `401 unauthenticated`,
  including unknown paths. Whether an operation is open is read from the
  spec (`security: []`), so the spec stays the source of truth.
- **No localhost exception.** Another local user or process mustn't get admin
  rights for free; the admin listener stays on 127.0.0.1 by default.
- **Rate limiting.** Failed sign-ins, setup codes and current-password checks
  count per username and per source IP: 5 free, then a doubling back-off up
  to 5 minutes (`429 too_many_attempts`, `Retry-After`), in memory, as for
  `/agent/unlock`.
- **The first account.** While no account exists the server makes a setup
  code (10 characters from an unambiguous alphabet, about 50 bits) at
  startup, logs it, and writes it to `<data>/setup-code.txt` (restricted like
  the secrets key). `POST /auth/setup` with the code creates the first account
  and signs it in; the file is deleted. A new code is made at each start
  until then. An upgraded server with no accounts goes through the same
  setup.
- **Recovery** offline, server stopped: `netdeployd admin reset-password
  -user <name>` asks for a new password twice and sets it (creating the
  account if it doesn't exist), ending its sessions.
- **Ending sessions at once.** When a password is changed or set, an
  account deleted, a session signed out or a token revoked, each open
  `/events` stream that belonged to it gets `session.ended` (with a reason)
  and closes; the UI shows the sign-in page straight away instead of at its
  next request. Other streams never see the event. Each stream also re-checks
  its session every heartbeat (15 s) without counting as activity, so idle and
  expired sessions end the same way, and a browser whose stream is refused
  (`401` after a restart) re-reads its session.
- **Idempotency keys** are scoped to the caller (account or token), so one
  caller can't replay another's response.
- **Development.** `-dev` drops `Secure` and the `__Host-` prefix from the
  cookie, so `just dev` works over plain HTTP on the LAN, and accepts an
  `Origin` matching `X-Forwarded-Host` (the Vite proxy rewrites `Host`). It
  raises the `dev_mode` health warning. `just dev` passes it.

## Consequences
- Every UI page needs a session; the UI sends people to sign in on `401` and
  back to where they were.
- Scripts written against the unauthenticated API need a token.
- Who did what is in the structured logs (`who=cookie:<user>` or
  `who=token:<user>/<token name>` on each admin request); deployments don't
  record who armed them yet.
- Deferred: AD/LDAP/OIDC sign-in, roles, an audit log, admin certificate
  renewal (packaging).
