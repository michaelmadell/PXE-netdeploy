# 9. Separate admin and content listeners

Status: accepted

## Context
Every machine on the deployment network must reach boot content. None of them
should reach the admin API.

## Decision
The admin listener (API + UI) is HTTPS-only, binds to `127.0.0.1:8443` by
default, and is opened to a management network explicitly. Content (boot files,
WIMs, agent API) is served per deployment interface over HTTP, optionally HTTPS.

## Consequences
- Exposing the admin UI remotely is a deliberate configuration step.
- The Vite dev server proxies to the admin listener during development.
