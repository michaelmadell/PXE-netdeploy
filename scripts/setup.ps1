#Requires -Version 7.2
<#
.SYNOPSIS
  First-time setup: checks prerequisites, installs codegen tools and frontend
  dependencies, then generates code. Safe to re-run.
#>
$ErrorActionPreference = 'Stop'
Set-Location (Join-Path $PSScriptRoot '..')

# Pinned tool versions. Bump deliberately and commit the regenerated code.
$OapiCodegen = 'github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.4.1'

function Assert-Command($name, $hint) {
    if (-not (Get-Command $name -ErrorAction SilentlyContinue)) {
        throw "$name was not found on PATH. $hint"
    }
}

function Assert-Version($label, [version]$actual, [version]$minimum) {
    if ($actual -lt $minimum) { throw "$label $actual is too old; need $minimum or newer." }
    Write-Host "  $label $actual" -ForegroundColor DarkGray
}

Write-Host 'Checking prerequisites' -ForegroundColor Cyan
Assert-Command go   'Install Go 1.24+ from https://go.dev/dl/'
Assert-Command node 'Install Node.js 22 LTS from https://nodejs.org/'
Assert-Command git  'Install Git from https://git-scm.com/'

$goVer = [regex]::Match((go env GOVERSION), '\d+\.\d+(\.\d+)?').Value
Assert-Version 'Go' $goVer '1.24'
Assert-Version 'Node' ((node --version).TrimStart('v')) '20.19'

if (-not (Get-Command pnpm -ErrorAction SilentlyContinue)) {
    Write-Host 'pnpm not found; enabling it via corepack' -ForegroundColor Yellow
    try { corepack enable pnpm } catch {
        throw "Could not enable pnpm with corepack (it may need an elevated shell on Windows). Run 'npm install -g pnpm' instead, then re-run setup."
    }
}

Write-Host 'Installing oapi-codegen as a Go tool dependency' -ForegroundColor Cyan
go get -tool $OapiCodegen
if ($LASTEXITCODE) { throw 'go get -tool failed' }

Write-Host 'Installing frontend dependencies' -ForegroundColor Cyan
pnpm --dir web install
if ($LASTEXITCODE) { throw 'pnpm install failed' }

Write-Host 'Generating code from api/openapi.yaml' -ForegroundColor Cyan
go tool oapi-codegen -config api/oapi-codegen.yaml api/openapi.yaml
if ($LASTEXITCODE) { throw 'oapi-codegen failed' }
pnpm --dir web gen:api
if ($LASTEXITCODE) { throw 'openapi-typescript failed' }
go mod tidy
if ($LASTEXITCODE) { throw 'go mod tidy failed' }

Write-Host 'Building to confirm everything compiles' -ForegroundColor Cyan
go build ./...
if ($LASTEXITCODE) { throw 'go build failed' }

Write-Host "`nSetup complete. Next: just dev" -ForegroundColor Green
