# 36. Linux deployment status: installer reports to the content listener

Status: accepted (2026-11-23). Phase 7 decision 5. Amends
[0034](0034-linux-clients.md) (how a Linux deployment ends) and builds on
[0035](0035-autoinstall.md) and [0026](0026-deployments.md) (stages,
timeouts).

## Context
The WinPE agent reports progress and completion (ADR 0025); a distribution
installer doesn't run our code. ADR 0034 left Linux deployments `running`
at 0% until the inactivity timeout failed them, even when the install
worked. The spike showed Subiquity's `reporting` webhook posting a JSON
event for every step (`name`, `description`, `event_type` start/finish,
`result`, `level`), about 150 a minute at its default level, and that
`curl` works in `early-commands` and `late-commands`.

## Decision
- **Where:** `POST /content/report/<token>/<kind>` on the content
  listeners, plain HTTP like the seed, authorised by the same deployment
  token (valid while the deployment runs; never logged). Kinds: `events`
  (the webhook), `started`, `finished`, `failed` (curl hooks).
- **Template data** gains `.Report` (`Webhook`, `Started`, `Finished`,
  `Failed`), built from the address the installer fetched its seed from.
  The built-in template sets `reporting: netdeploy: {type: webhook}`, curls
  `Started` in `early-commands`, `Failed` in `error-commands`, and
  `Finished` as the last `late-commands` entry. Checks warn when a
  template has no webhook to `.Report.Webhook` or never calls
  `.Report.Finished`.
- **Progress:** the start of known steps moves the deployment, never back,
  through the existing stages: boot commands served (`checked-in` 2%),
  `user-data` fetched (5%), `started` (8%), applying the settings (10%),
  apt (15%), `partitioning` (20%), `applying-image` (the system written to
  disk, 30%), `configuring` (curthooks 55%, boot packages 60%, kernel 70%,
  package updates 80%), `finalizing` (postinstall 90%, late-commands 95%).
  The message is the step in words.
- **End:** `finished` marks the deployment `succeeded` ("installed;
  rebooting"); `failed`, or a webhook `finish` with `result: FAIL` under
  `subiquity/Install/install`, marks it `failed`. Failures of other steps
  are logged only.
- **Logs:** `started`, `finished`, `failed`, and webhook events at `INFO` or
  louder (or failed) go to the deployment's log (source `installer`);
  `DEBUG` events only move progress.
- **Inactivity:** every report counts as activity, recorded at most every
  30 s, so the ADR 0026 inactivity timeout keeps working for a stuck
  installer.
- **Interactive installs** can't report, so their deployment is marked
  `succeeded` when the boot commands are served ("the installer started;
  interactive installs aren't tracked further") instead of failing at the
  inactivity timeout.

## Consequences
- A deployment's success means the installer finished, not that the
  installed system booted; a first-boot check-in would be a further step.
- The stage percentages are estimates from the spike's timings and step
  names (Subiquity 26.04, curtin 24); renamed steps would only leave a
  stage unmoved, never fail a deployment.
- Anyone with a running deployment's token can post reports for it (mark
  it succeeded or failed); the token crosses the network in clear, as the
  seed URL does.
- Custom templates must keep the report hooks to be tracked; without
  `Finished` a deployment ends at the inactivity timeout.
- To verify in the lab: the stages and messages during a NUC install,
  `succeeded` at the end, `failed` from a deliberately broken late command.
