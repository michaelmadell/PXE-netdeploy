# 3. Go for the server and the WinPE agent

Status: accepted

## Context
The server runs DHCP, TFTP and HTTP concurrently on Windows and Linux hosts. The
agent runs inside WinPE.

## Decision
Write both in Go. The server is one static binary per OS (no cgo; SQLite via
`modernc.org/sqlite`) with the UI embedded. The agent is a Go executable, so
WinPE needs no PowerShell, WMI or .NET optional components.

## Consequences
- Cross-compiling every target from either host OS.
- Agent and server share the generated API types.
- Per-site agent customisation needs a rebuild rather than script edits.
