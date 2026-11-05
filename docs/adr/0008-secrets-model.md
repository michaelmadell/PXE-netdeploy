# 8. Secrets are write-only and only decrypted for an agent

Status: accepted

## Context
Profiles need admin passwords, product keys, domain join credentials, ODJ blobs,
Autopilot files and provisioning packages.

## Decision
A `secrets` resource: values are write-only through the admin API, encrypted at
rest (DPAPI on Windows, a key file on Linux), and decrypted only when rendering
for an authenticated agent's deployment. Previews show `********`. Deployments
snapshot secret versions. Offline domain join is recommended over credential joins.

## Consequences
- Unattend files are still plain text on the client during setup; the agent
  installs a cleanup step that deletes them.
- MAC spoofing on the deployment LAN could obtain a pending deployment's config;
  documented in the agent check-in threat model.
