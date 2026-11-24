#Requires -Version 7.2
<#
.SYNOPSIS
  The version for builds and packages (ADR 0031), from git tags: v0.6.0 gives
  0.6.0; three commits later 0.6.0-3-gabc1234; uncommitted changes add
  -dirty. Pre-release tags (v0.1.0-rc.1, -beta.2, -alpha.3) give 0.1.0-rc.1.
  Before the first tag: 0.0.0-<commits>-g<hash>. Returns Version, Deb
  (sorts correctly in dpkg: 0.6.0~dev3+gabc1234; 0.1.0~rc.1 before 0.1.0)
  and File (numeric, for Windows: 0.6.0.3).
.PARAMETER Described
  A `git describe` result to use instead of the repository's (for testing).
#>
param([string]$Described)
if (-not $Described) { $Described = (git describe --tags --always --dirty 2>$null) }
if (-not $Described) { return [pscustomobject]@{ Version = 'dev'; Deb = '0.0.0~dev'; File = '0.0.0.0' } }
$v = $Described -replace '^v', ''
$pre = $null
if ($v -match '^(\d+\.\d+\.\d+)(?:-((?:alpha|beta|rc)\.\d+))?(?:-(\d+)-g([0-9a-f]+))?(-dirty)?$') {
    $base, $pre, $n, $hash, $dirty = $Matches[1], $Matches[2], $Matches[3], $Matches[4], $Matches[5]
} else {
    $base, $n = '0.0.0', (git rev-list --count HEAD)
    $hash = $v -replace '-dirty$', ''
    $dirty = $v.EndsWith('-dirty') ? '-dirty' : $null
    $v = "0.0.0-$n-g$hash$dirty"
}
$deb = $base
if ($pre) {
    # ~ sorts before anything, so 0.1.0~rc.1 < 0.1.0; later commits on the
    # pre-release sort after it (+), still before the release.
    $deb += "~$pre"
    if ($n) { $deb += "+dev$n.g$hash" }
} elseif ($n) {
    $deb += "~dev$n+g$hash"
}
if ($dirty) { $deb += (($n -or $pre) ? '.dirty' : '+dirty') }
[pscustomobject]@{ Version = $v; Deb = $deb; File = "$base.$([int]($n ?? 0))"; Prerelease = [bool]$pre }
