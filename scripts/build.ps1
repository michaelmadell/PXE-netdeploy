#Requires -Version 7.2
<#
.SYNOPSIS
  Release build into ./dist: the server for Windows and Linux with the UI
  embedded, and the WinPE agent for x64 and arm64.
#>
$ErrorActionPreference = 'Stop'
Set-Location (Join-Path $PSScriptRoot '..')

$version = (git describe --tags --always --dirty 2>$null)
if (-not $version) { $version = 'dev' }
Write-Host "Version $version" -ForegroundColor Cyan

pnpm --dir web build
if ($LASTEXITCODE) { throw 'frontend build failed' }

New-Item -ItemType Directory -Force dist | Out-Null
$ldflags = "-s -w -X main.version=$version"
$env:CGO_ENABLED = '0'

# Agents first: the server embeds them (internal/agentbin, -tags agent; ADR 0022).
$targets = @(
    @{ Pkg = './cmd/agent';      Name = 'agent';      Os = 'windows'; Arch = 'amd64'; Tags = '' }
    @{ Pkg = './cmd/agent';      Name = 'agent';      Os = 'windows'; Arch = 'arm64'; Tags = '' }
    @{ Pkg = './cmd/netdeployd'; Name = 'netdeployd'; Os = 'windows'; Arch = 'amd64'; Tags = 'ui agent' }
    @{ Pkg = './cmd/netdeployd'; Name = 'netdeployd'; Os = 'linux';   Arch = 'amd64'; Tags = 'ui agent' }
    @{ Pkg = './cmd/netdeployd'; Name = 'netdeployd'; Os = 'linux';   Arch = 'arm64'; Tags = 'ui agent' }
)
New-Item -ItemType Directory -Force internal/agentbin/bin | Out-Null

try {
    foreach ($t in $targets) {
        $ext = $t.Os -eq 'windows' ? '.exe' : ''
        $out = "dist/$($t.Name)-$($t.Os)-$($t.Arch)$ext"
        $env:GOOS = $t.Os; $env:GOARCH = $t.Arch
        Write-Host "  $out"
        go build -trimpath -tags "$($t.Tags)" -ldflags $ldflags -o $out $t.Pkg
        if ($LASTEXITCODE) { throw "build failed: $out" }
        if ($t.Name -eq 'agent') { Copy-Item $out "internal/agentbin/bin/agent-$($t.Os)-$($t.Arch).exe" }
    }
}
finally {
    Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue
}

Write-Host 'Done.' -ForegroundColor Green
