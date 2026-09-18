param(
    [string]$ProjectPath,
    [string]$GamePath,
    [string]$OutputPath,
    [string]$ModsPath,
    [string]$Configuration = 'Debug'
)

$ErrorActionPreference = 'Stop'

# This script only knows the adapter project's location and the install target.
# It makes no assumption about the surrounding repository layout, so the adapter
# can be built and installed from any directory.
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path

if (-not $ProjectPath) {
    $ProjectPath = Join-Path $repoRoot 'adapters\stardew\GameAgent.Stardew.csproj'
}
if (-not $GamePath) {
    $GamePath = (Resolve-Path (Join-Path $repoRoot '..\Stardew Valley')).Path
}

if (-not (Test-Path -LiteralPath $ProjectPath)) {
    throw "Stardew adapter project not found: $ProjectPath"
}

$projectDirectory = Split-Path -Parent $ProjectPath
if (-not $OutputPath) {
    $OutputPath = Join-Path $projectDirectory "bin\$Configuration"
}

if (-not $ModsPath) {
    if (-not (Test-Path -LiteralPath $GamePath)) {
        throw "Stardew install path not found: $GamePath"
    }
    $ModsPath = Join-Path $GamePath 'Mods'
}

if (-not (Test-Path -LiteralPath $ModsPath)) {
    throw "Stardew Mods directory not found: $ModsPath"
}

$targetPath = Join-Path $ModsPath 'GameAgentStardew'

Write-Host "Building Stardew adapter ($Configuration)..."
dotnet build $ProjectPath --configuration $Configuration -p:GamePath="$GamePath"
if ($LASTEXITCODE -ne 0) {
    throw "dotnet build failed with exit code $LASTEXITCODE"
}

if (-not (Test-Path -LiteralPath $OutputPath)) {
    throw "Build output not found: $OutputPath"
}

New-Item -ItemType Directory -Force -Path $targetPath | Out-Null

Write-Host "Installing adapter to: $targetPath"
Copy-Item -Path (Join-Path $OutputPath '*') -Destination $targetPath -Recurse -Force

Write-Host 'Stardew adapter installed.'
