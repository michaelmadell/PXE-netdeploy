# netdeploy

Network deployment for Windows 11 (and later other operating systems) over PXE
and UEFI HTTP Boot, working with standard Secure Boot: Microsoft-signed boot
managers, both the 2011 and 2023 CA chains, and no custom keys on the clients.

WDS-style workflow: import ISOs and driver packs, build boot images with the
drivers your hardware needs (an Intel I210 NIC, say), combine them into
profiles, and deploy to registered machines. Runs on Windows or Linux as a
single binary with a web UI and a documented API.

> **Status:** early scaffolding. The API spec is drafted; the server currently
> serves the UI and a health endpoint. See [the roadmap](docs/architecture.md#roadmap).

## Quick start

Prerequisites: [Go 1.24+](https://go.dev/dl/), [Node.js 22 LTS](https://nodejs.org/),
[PowerShell 7](https://aka.ms/powershell), [just](https://just.systems), Git.

```sh
just setup   # check tools, install dependencies, generate code, build
just dev     # server on https://127.0.0.1:8443, UI on http://localhost:5173
```

On first run the server creates `./data` and a self-signed admin certificate.

## Common tasks

| Command | What it does |
|---|---|
| `just gen` | Regenerate Go server code and the TypeScript client from `api/openapi.yaml` |
| `just lint` | Lint the spec, vet Go, typecheck the frontend |
| `just test` | Run Go tests |
| `just build` | Release binaries in `./dist` (server with embedded UI, WinPE agents) |
| `just lab` | Create a Hyper-V test VM and lab switch (Windows, elevated) |

## Repository layout

```
api/            OpenAPI spec (source of truth) and codegen config
cmd/netdeployd  Server entry point
cmd/agent       WinPE deployment agent
internal/api    Generated server code — do not edit
internal/server API implementation and HTTP middleware
internal/certs  Admin TLS certificate handling
web/            React frontend (Vite, Tailwind v4, shadcn/ui); web/src/api/schema.d.ts is generated
scripts/        PowerShell 7 scripts behind the justfile recipes
docs/           Architecture, developer guides, and decision records (ADRs)
```

## Documentation

- [Project brief](docs/PROJECT_BRIEF.md): start here; status, open questions, next steps
- [Phase 1 spike plan](docs/plan/phase-1-spike.md)
- [Architecture](docs/architecture.md): components, boot flows, roadmap
- [Getting started](docs/dev/getting-started.md): setup and the add-an-endpoint workflow
- [Test environments](docs/dev/test-environments.md): Hyper-V lab, QEMU/OVMF with custom keys
- [Decision records](docs/adr/): why things are the way they are
