# 7. Agent install by default, SMB optional

Status: accepted

## Context
WinPE needs the install source after boot. WDS uses SMB; serving SMB from Go is
not production-grade.

## Decision
Profiles choose `installMethod`:
- **agent**: the agent downloads `install.wim/esd` over HTTP and applies it with DISM.
- **smb**: the agent runs `setup.exe` from an existing share (Windows file share
  or Samba). netdeployd writes content to the share and stores its credentials;
  it does not run an SMB server.

## Consequences
- No SMB dependency for the default path; SMB available where teams already use it.
- One unattend template renders for both (agent mode strips the `windowsPE` pass).
