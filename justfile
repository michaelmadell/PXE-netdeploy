# netdeploy task runner. Install `just` (https://just.systems) and PowerShell 7 (`pwsh`).
# Every recipe runs in pwsh, so the same commands work on Windows and Linux.

set shell := ["pwsh", "-NoLogo", "-NoProfile", "-Command"]
set windows-shell := ["pwsh", "-NoLogo", "-NoProfile", "-Command"]

# List recipes
default:
    @just --list

# First-time setup: check prerequisites, install tools and dependencies, generate code
setup:
    ./scripts/setup.ps1

# Regenerate Go server code and the TypeScript client from api/openapi.yaml
gen:
    go tool oapi-codegen -config api/oapi-codegen.yaml api/openapi.yaml
    pnpm --dir web gen:api
    go mod tidy

# Lint the spec, vet Go, typecheck the frontend
lint:
    pnpm --dir web lint:spec
    go vet ./...
    pnpm --dir web typecheck

# Run Go tests
test:
    go test ./...

# Run the server and the Vite dev server together (Ctrl+C stops both)
dev:
    ./scripts/dev.ps1

# Build the WinPE agents into ./dist (a server built without -tags agent reads them from there)
agent:
    ./scripts/build-agent.ps1

# Run only the Go server (rebuilds the agents first)
dev-server: agent
    go run ./cmd/netdeployd -data ./data

# Run only the Vite dev server (proxies /api to https://127.0.0.1:8443)
dev-web:
    pnpm --dir web dev

# Release build: frontend embedded in the server, plus agents, into ./dist
build:
    ./scripts/build.ps1

# Fail if generated code is out of date with the spec (used in CI)
check-drift: gen
    git diff --exit-code -- internal/api web/src/api/schema.d.ts go.mod go.sum

# Remove build output
clean:
    Remove-Item -Recurse -Force -ErrorAction SilentlyContinue dist, web/dist, .tmp

# Create or remove the Hyper-V test lab (Windows, elevated). Example: just lab -SecureBootTemplate MicrosoftUEFICertificateAuthority
lab *args:
    ./scripts/hyperv-lab.ps1 {{args}}
