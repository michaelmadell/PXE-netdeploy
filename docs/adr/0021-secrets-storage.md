# 21. Secrets storage: one data key, protected by DPAPI or a key file

Status: accepted

## Context
ADR 0008 decided that secrets are write-only and encrypted at rest (DPAPI on
Windows, a key file on Linux). Backups to a network share (ADR 0020) need a
stored credential in Phase 3, before profiles use secrets in Phase 4.
Backups must not make secrets readable to whoever holds a backup, yet a
restore onto new hardware has to be possible.

## Decision
- **Envelope**: one random 256-bit data key per installation encrypts each
  value with AES-256-GCM. The associated data binds the ciphertext to its
  secret id, kind and value version, so swapping rows doesn't decrypt.
- **The data key** lives in `<data>/secrets.key`, created on first use:
  - Windows: wrapped with DPAPI in machine scope (survives a change of
    service account); the file's ACL allows only SYSTEM, Administrators and
    the account that created it, since machine-scope DPAPI lets any local
    account that can read the file unwrap it.
  - Linux: the raw key, mode 0600, owned by the service user.
- **Backups exclude the key**; the manifest records its fingerprint
  (SHA-256 of the key). Restore on the same installation finds the key
  in place; anywhere else needs an export.
- **Export/import**: `netdeployd key export -out <file>` writes the key
  encrypted with a passphrase (Argon2id, 3 passes, 64 MiB, as for the boot
  prompt password; AES-256-GCM; at least 12 characters, asked twice). `netdeployd key import` and
  `restore -key <file>` read it back (and count as an export: whoever
  imports holds one). Health warns `secrets_key_not_exported` once any
  secret exists until the current key has been exported.
- **API**: `/secrets` as specified, plus a `share-credential` kind (username
  and password; the username is the hint). Values are never returned.
  File-based values (ODJ blob, Autopilot JSON, provisioning package) are at
  most 16 MiB and their upload is deleted as soon as it's read, so no
  plaintext stays in `uploads/`.
  Deleting a secret referenced by a client, profile, SMB target or the backup
  settings fails with `409 secret_in_use`.
- **SMB targets** reference a credential secret (`credentialSecretId`)
  instead of holding a username and password, so every stored credential is
  a secret.
- Decryption happens only where a value is used: the backup job (share
  credential) now, agent rendering from Phase 4.

## Consequences
- Losing both the machine and the key export loses the secrets (not the rest
  of a restore); they're re-entered.
- Reinstalling Windows on the same hardware loses the DPAPI machine key, so
  it counts as new hardware.
- No key rotation yet; if needed it becomes a command that re-encrypts every
  row under a new key.
