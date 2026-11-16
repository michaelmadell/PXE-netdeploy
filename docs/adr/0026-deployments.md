# 26. Deployments: arming, snapshots, timeouts, cancelling, built-in unattend

Status: accepted (2026-11-12). Builds on [0008](0008-secrets-model.md),
[0013](0013-boot-prompt-password-gate.md), [0021](0021-secrets-storage.md),
[0024](0024-booter.md) and [0025](0025-agent.md).

## Context
ADR 0025 runs a deployment started at the machine's boot prompt. Still
missing: arming a client from the admin API, what a deployment snapshots,
timeouts, cancelling, an unattend file so Setup finishes without questions,
and checking a profile before it's used. Unattend templates are Phase 5;
Phase 4 needs a built-in one per identity mode.

## Decision
**Arming** (`POST /deployments`):
- Profile: the body's `profileId`, else the client's. Neither: `422
  no_profile` (arming never falls back to the boot prompt's default).
- The profile is validated as `POST /profiles/{id}/validate` does; any
  error finding refuses with `422 profile_invalid`, the findings in
  `errors[]`. `409 deployment_active` as before.
- The deployment snapshots the profile, the rendered computer name and
  copies of the secrets it uses (below). A pending deployment's client boots
  the boot image the snapshot names. Deleting an install image a pending
  deployment uses is refused (`409 image_in_use`); running ones already keep
  their files (`blob_refs`).
- Starting from the boot prompt snapshots the same way; a failure (invalid
  computer name, missing ODJ blob) is refused before the agent wipes
  anything and shown on its screen.

**Secret snapshots**: a secret's ciphertext is bound to its id, kind and
version (ADR 0021), so it is copied as is into `deployment_secrets`; nothing
is decrypted when arming. Rotating or deleting the secret later doesn't
change the deployment. The copies are deleted when the deployment finishes.

**Status and timeouts** (`Settings.deployments`):

| From | To | When |
|---|---|---|
| pending | running | agent check-in (ADR 0025) |
| pending | cancelled | cancel |
| pending | failed (`expired`) | not picked up within `armedExpiryHours` (72) |
| running | succeeded / failed | agent `complete` |
| running | failed (`restarted`) | agent checks in again (ADR 0025) |
| running | failed (`timeout`) | no agent request for `inactivityMinutes` (30) |
| running | cancelled | cancel |

The agent reports progress at least once a minute, so a long download or
DISM run is never silent. The last agent request time is stored, so a
server restart doesn't fail running deployments (the agent retries); a
sweep every minute applies the timeouts.

**Cancelling a running deployment** stops it gracefully. The server marks it
`cancelled` at once; the agent learns at its next request (`409
deployment_cancelled` on progress; within a minute). It never kills a
running command: the current diskpart script, `dism /Apply-Image`,
`dism /Add-Driver`, `bcdboot` or `reagentc` finishes, then the agent stops
before the next step. Its own work (downloads, file copies) stops at once
and partial files are deleted. It does not change the boot order, uploads
its logs, acknowledges with `complete` (`success: false`), shows
"Cancelled" and reboots. For that, a cancelled deployment's token still
works for logs and complete for 30 minutes. The disk may be left
partitioned or with an image and no boot files.

**Built-in unattend** (`GET /agent/unattend`, the plan's `unattendUrl`):
rendered on each request from the snapshot and the secret copies, with the
deployment token, while it runs; never stored. The agent writes it to
`Windows\Panther\unattend.xml`, deleted after Setup (ADR 0008).
- All modes: computer name (`*` when there is no pattern), time zone, UI
  language, locales, keyboard, product key, EULA page hidden.
- `none`: OOBE stays interactive.
- `local`: the accounts (group, password in unattend's encoded form),
  optional auto-logon, and `hideOobe` hides the account, online-account and
  privacy screens.
- `ad`, `joinMethod: credentials`: `UnattendedJoin` in specialize (domain,
  OU, the `domain-credential` secret); needs a DC reachable during Setup.
- `ad`, `joinMethod: odj`: the client's `secretRefs.odjBlob` (kind
  `odj-blob`, a `djoin /provision /savefile` file, checked when the secret
  is created) goes into the unattend as `UnattendedJoin/Identification/
  Provisioning/AccountData`; Setup joins offline in specialize, with no DC.
  The blob sets the computer name, so the unattend has no `ComputerName`; a
  profile pattern is ignored (validate warns). Arming a client without a
  blob: `422 missing_odj_blob`. (Amended 2026-11-16: the agent was to run
  `djoin /requestODJ` against the applied image, but WinPE has no
  `djoin.exe`; `GET /agent/identity/{kind}` was removed with it.)
- `ad` may add the optional break-glass local administrator.
- `entra`: Phase 5 (`422 not_implemented` on profiles).

**Computer name**: `osSettings.computerNamePattern` is a Go template
rendered when a deployment is armed or started: `.Client.Serial`, `.MAC`,
`.SMBIOSUUID`, `.Hostname`, `.Variables` (profile then client), and the
functions `first n`, `last n`, `upper`, `lower`. The result must be 1–15
letters, digits or hyphens, not all digits, no hyphen at either end (`422
invalid_computer_name`). A pattern that uses a client field the client
lacks (an empty serial) is refused too, rather than rendering `LAB-`.

**Validation** (`POST /profiles/{id}/validate`, also run when arming):
- errors: boot image not built, failed or out of date; install image not
  ready or edition missing; `smb` or `entra`; a missing secret or one of the
  wrong kind; `ad` credentials without a domain or credential; an
  `autoLogon` account not in `accounts`; a computer name pattern that
  doesn't parse.
- warnings: only a 2011-signed boot manager; boot image TFTP values above
  the server caps (`tftp_capped_by_server`); per assigned client, a
  rendered computer name that breaks the rules, or (`odj`) no ODJ blob; a
  name pattern with `odj`.

## Consequences
- A deployment holds copies of encrypted secrets until it finishes; the
  data key still protects them (ADR 0021).
- Cancelling can leave a disk unbootable; the confirm dialog says so.
- `credentials` joins put the join account's password on the machine
  until Setup's cleanup deletes the unattend file; `odj` is preferred
  (ADR 0008).
- Entra/Autopilot and unattend templates remain Phase 5.
