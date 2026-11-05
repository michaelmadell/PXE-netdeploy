# Getting started

## Prerequisites

| Tool | Version | Notes |
|---|---|---|
| Go | 1.24+ | `go tool` directive is used for oapi-codegen |
| Node.js | 22 LTS (20.19+ works) | |
| pnpm | 10 | `just setup` enables it via corepack if missing |
| PowerShell | 7.2+ | All scripts and `just` recipes run in `pwsh`, on Windows and Linux |
| just | any recent | Task runner |
| Git | any | |

## First run

```sh
just setup
just dev
```

Open http://localhost:5173. Vite proxies `/api` to the Go server on
https://127.0.0.1:8443 (self-signed certificate; your browser will warn if you
open the server address directly).

Running DHCP or TFTP later: on Windows, ports 67 and 69 can be bound without
elevation, but creating firewall rules needs it (`just lab` adds rules for the
lab adapter). On Linux they are privileged ports, so run with root or grant
`CAP_NET_BIND_SERVICE` to the binary.

## The spec is the source of truth

All API changes start in `api/openapi.yaml`. Generated code is committed and CI
fails if it drifts from the spec.

### Adding or implementing an operation

1. Edit `api/openapi.yaml` (if the operation is new or changing).
2. `just gen`: regenerates `internal/api/api.gen.go` and `web/src/api/schema.d.ts`.
3. `just lint` to catch spec mistakes early.
4. Implement it as a method on `*Server` in `internal/server`. The method name
   is the `operationId` in PascalCase; copy the signature from
   `StrictServerInterface` in `api.gen.go`. Until a method exists, calling the
   operation returns a 500 and logs a panic. That is deliberate, see `server.go`.
5. Use it in the frontend through the typed client:
   ```ts
   const { data, error } = await api.GET("/images", { params: { query: { limit: 50 } } });
   ```
6. Commit the spec, generated code, and implementation together.

### Adding shadcn/ui components

```sh
cd web
pnpm dlx shadcn@latest add button table dialog
```

Components land in `web/src/components/ui` and use the theme tokens in `src/index.css`.

## Conventions

- Errors: RFC 9457 problem JSON with a stable `code`. Never put internal error
  text in responses; log it.
- Logging: `log/slog`, one structured line per request.
- Windows-specific code goes in `_windows.go` files with a portable counterpart,
  so `go build ./...` works on every host.
