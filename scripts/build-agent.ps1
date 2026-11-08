#Requires -Version 7.2
<#
.SYNOPSIS
  Builds the WinPE agent for x64 and arm64 into ./dist, where a server built
  without -tags agent finds it (-agent-dir, ADR 0022). Run it after changing
  cmd/agent, then rebuild boot images (they go stale: reason "agent").
#>
$ErrorActionPreference = 'Stop'
Set-Location (Join-Path $PSScriptRoot '..')
New-Item -ItemType Directory -Force dist | Out-Null
$env:CGO_ENABLED = '0'
try {
    foreach ($arch in 'amd64', 'arm64') {
        $env:GOOS = 'windows'; $env:GOARCH = $arch
        go build -trimpath -o "dist/agent-windows-$arch.exe" ./cmd/agent
        if ($LASTEXITCODE) { throw "agent build failed ($arch)" }
    }
}
finally {
    Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue
}
Write-Host 'Agents built into dist/' -ForegroundColor Green
