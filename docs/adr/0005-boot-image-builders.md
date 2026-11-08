# 5. Two boot image builders: DISM and wimlib

Status: accepted; refined by [0022](0022-building-boot-images.md), which
allows storage drivers with wimlib (WinPE runs from a RAM disk, so the
agent can `drvload` them) and makes `auto` prefer wimlib.

## Context
Devices such as the Intel I210 need their NIC driver inside WinPE before the
agent can use the network, so boot images must be customised. The server runs on
both Windows and Linux hosts.

## Decision
- **dism** (Windows hosts): offline driver injection and optional components.
- **wimlib** (any host): copies driver files into the image; the agent loads them
  with `drvload` before networking. Not suitable for boot-critical storage drivers.

`builder: auto` prefers DISM when available. `/system/capabilities` reports what
the host has; validation flags wimlib images that contain storage drivers.
Builds are cached by a hash of all inputs and run one at a time per host.

## Consequences
- Linux hosts can serve I210-class hardware without a Windows build machine.
- Optional components (PowerShell in WinPE, etc.) need a Windows host with a matching ADK.
