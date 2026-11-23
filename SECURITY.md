# Security policy

## Reporting a vulnerability

Please report security problems **privately** through GitHub's
[private vulnerability reporting](https://github.com/michaelmadell/PXE-netdeploy/security/advisories/new)
(the repository's Security tab → Report a vulnerability). Don't open a public
issue.

Include what's affected, how to reproduce it, and the impact you see. You'll
get an acknowledgement within a week; a fix and a coordinated disclosure are
agreed with you before anything is published.

## Supported versions

netdeploy is pre-release (0.x). Fixes go into the next release only; there
are no back-ports to older 0.x versions.

## Security model

What netdeploy protects, and what it assumes (details in the
[decision records](docs/adr/README.md)):

- **The admin UI and API** are HTTPS only, behind named accounts (argon2id
  passwords, session cookies) or API tokens; they listen on `localhost` by
  default (ADR 0009, 0029).
- **Secrets** (passwords, domain credentials, offline join blobs) are
  encrypted at rest under a key protected by DPAPI or a 0600 key file, are
  never shown again once saved, and are decrypted only for a running
  deployment (ADR 0008, 0021).
- **Clients only install when asked:** an armed deployment, or the boot
  prompt password typed at the machine (ADR 0013, 0034).
- **The deployment network is trusted.** Content is served over plain HTTP
  unless content HTTPS is configured (ADR 0028), and some steps can't use
  HTTPS at all: firmware PXE/TFTP, and the Ubuntu installer's ISO download,
  NoCloud seed (which holds the account's SHA-512 password hash, never the
  password) and progress reports (ADR 0035, 0036). Per-deployment tokens in
  those URLs expire when the deployment ends. The boot prompt password
  crosses the network in clear unless content HTTPS is on.
- **Release binaries aren't code-signed yet**; verify downloads against the
  release's checksums.
