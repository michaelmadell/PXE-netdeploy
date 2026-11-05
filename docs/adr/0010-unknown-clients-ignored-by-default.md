# 10. Unknown clients are ignored by default

Status: accepted

## Context
A proxy DHCP server answers PXE requests from any machine on the segment. On a
shared network that can re-image a machine nobody meant to touch.

## Decision
`unknownClients.policy` defaults to `ignore`: machines without a pending
deployment get no answer and fall through to their next boot device. `register`
records them for approval (like WDS pending devices); `default-profile` is opt-in.

## Consequences
Lab setups that want zero-touch must opt in explicitly.
