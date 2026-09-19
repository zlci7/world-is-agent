<#
.SYNOPSIS
Build the portable Runtime package: one executable, a README and the license.

.DESCRIPTION
The package is meant to be extracted anywhere and run with no arguments, so this
script builds the client into the binary, builds the Runtime with the version from
VERSION, and assembles a directory that contains nothing but what a user needs.
It does not build the game adapter: adapters are installed separately, as mods.

.EXAMPLE
.\scripts\release-runtime.ps1
.EXAMPLE
.\scripts\release-runtime.ps1 -Version 0.2.0
#>
param(
    # Overrides VERSION for a one-off build. The file stays the source of truth.
    [string]$Version,
    # Where the package is assembled. Defaults to a temporary directory, because
    # a build output does not belong in the repository.
    [string]$StageDir,
    # Where the archive is written. dist/ is ignored by git.
    [string]$OutputDir
)

$ErrorActionPreference = 'Stop'
$root = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path

if (-not $Version) {
    $versionFile = Join-Path $root 'VERSION'
    if (-not (Test-Path -LiteralPath $versionFile -PathType Leaf)) {
        throw "VERSION file not found: $versionFile"
    }
    $Version = (Get-Content -LiteralPath $versionFile -Raw -Encoding UTF8).Trim()
}
if (-not $Version) {
    throw 'A version is required: put one in VERSION or pass -Version.'
}

if (-not $StageDir) {
    $StageDir = Join-Path ([System.IO.Path]::GetTempPath()) "wia-release-$Version"
}
if (-not $OutputDir) {
    $OutputDir = Join-Path $root 'dist'
}

Get-Command go -ErrorAction Stop | Out-Null

$packageName = "world-is-agent-v$Version-windows-amd64"
$stage = Join-Path $StageDir $packageName
if (Test-Path -LiteralPath $stage) {
    Remove-Item -LiteralPath $stage -Recurse -Force
}
New-Item -ItemType Directory -Path $stage -Force | Out-Null

$exeName = 'wia-runtime.exe'

# The client is embedded with //go:embed, so it has to exist before the Go build
# reads it. Building it here keeps the package from shipping a stale bundle.
Write-Host 'Building the local client...'
Push-Location (Join-Path $root 'console\web')
try {
    if (-not (Test-Path -LiteralPath 'node_modules')) {
        npm ci
    }
    npm run build
    if ($LASTEXITCODE -ne 0) { throw "Client build failed with exit code $LASTEXITCODE" }
}
finally {
    Pop-Location
}

Write-Host "Building $exeName (version $Version)..."
Push-Location $root
try {
    # -s -w drop the symbol table and DWARF data; the package is smaller and
    # nothing here is meant to be debugged from a release binary.
    # CGO_ENABLED=0 keeps the binary free of a C runtime dependency.
    $env:CGO_ENABLED = '0'
    & go build -trimpath -ldflags "-s -w -X main.version=$Version" -o (Join-Path $stage $exeName) ./runtime/cmd/server
    if ($LASTEXITCODE -ne 0) { throw "Go build failed with exit code $LASTEXITCODE" }
}
finally {
    $env:CGO_ENABLED = $null
    Pop-Location
}

Copy-Item -LiteralPath (Join-Path $root 'deploy\release\README.txt') -Destination $stage
Copy-Item -LiteralPath (Join-Path $root 'LICENSE') -Destination $stage

New-Item -ItemType Directory -Path $OutputDir -Force | Out-Null
$archive = Join-Path $OutputDir "$packageName.zip"
if (Test-Path -LiteralPath $archive) {
    Remove-Item -LiteralPath $archive -Force
}
Compress-Archive -Path $stage -DestinationPath $archive -CompressionLevel Optimal

Write-Host ''
Write-Host "Package:  $archive"
Write-Host "Size:     $([math]::Round((Get-Item -LiteralPath $archive).Length / 1MB, 1)) MB"
Write-Host 'Contents:'
Get-ChildItem -LiteralPath $stage | ForEach-Object {
    Write-Host ("  {0,-20} {1,10:N0} bytes" -f $_.Name, $_.Length)
}
