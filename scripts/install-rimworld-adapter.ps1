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
    $ProjectPath = Join-Path $repoRoot 'adapters\rimworld\WiaRimWorld.csproj'
}

# The game is not a build dependency: Krafs.Rimworld.Ref supplies reference assemblies.
# It is only needed to resolve the install target, so an explicit path always wins.
if (-not $GamePath) { $GamePath = $env:WIA_RIMWORLD_GAME_PATH }
if (-not $GamePath) { $GamePath = Join-Path $repoRoot '..\RimWorld' }

if (-not (Test-Path -LiteralPath $ProjectPath)) {
    throw "RimWorld adapter project not found: $ProjectPath"
}

$projectDirectory = Split-Path -Parent $ProjectPath
if (-not $OutputPath) {
    $OutputPath = Join-Path $projectDirectory "bin\$Configuration\net472"
}

if (-not $ModsPath) {
    if (-not (Test-Path -LiteralPath $GamePath)) {
        throw "RimWorld install path not found: $GamePath. Pass -GamePath or set WIA_RIMWORLD_GAME_PATH."
    }
    if (-not (Test-Path -LiteralPath (Join-Path $GamePath 'RimWorldWin64.exe'))) {
        throw "Not a RimWorld install: $GamePath"
    }
    $ModsPath = Join-Path $GamePath 'Mods'
}

if (-not (Test-Path -LiteralPath $ModsPath)) {
    throw "RimWorld Mods directory not found: $ModsPath"
}

$targetPath = Join-Path $ModsPath 'WiaRimWorld'

# RimWorld hands every *.dll in Assemblies/ to Assembly.LoadFrom, so the staging list is explicit:
# the adapter assembly and its managed dependencies only. The build output is never copied wholesale
# because it also contains x86, macOS and Linux native libraries and a pdb.
$managed = @(
    'WiaRimWorld.dll',
    'Google.Protobuf.dll',
    'Grpc.Core.dll',
    'Grpc.Core.Api.dll',
    'System.Memory.dll',
    'System.Buffers.dll',
    'System.Numerics.Vectors.dll',
    'System.Runtime.CompilerServices.Unsafe.dll'
)

Write-Host "Building RimWorld adapter ($Configuration)..."
dotnet build $ProjectPath --configuration $Configuration
if ($LASTEXITCODE -ne 0) {
    throw "dotnet build failed with exit code $LASTEXITCODE"
}

if (-not (Test-Path -LiteralPath $OutputPath)) {
    throw "Build output not found: $OutputPath"
}

Write-Host "Installing adapter to: $targetPath"
New-Item -ItemType Directory -Force -Path (Join-Path $targetPath 'About') | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $targetPath 'Assemblies') | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $targetPath 'Native') | Out-Null

Copy-Item -LiteralPath (Join-Path $projectDirectory 'About\About.xml') -Destination (Join-Path $targetPath 'About\About.xml') -Force

foreach ($name in $managed) {
    $source = Join-Path $OutputPath $name
    if (-not (Test-Path -LiteralPath $source)) {
        throw "Missing build output: $source"
    }
    Copy-Item -LiteralPath $source -Destination (Join-Path $targetPath "Assemblies\$name") -Force
}

# Clear any native image a previous layout left in Assemblies/.
Get-ChildItem -LiteralPath (Join-Path $targetPath 'Assemblies') -Filter 'grpc_csharp_ext*' -ErrorAction SilentlyContinue |
    Remove-Item -Force

# The native library must sit outside Assemblies/, and must be named exactly as the DllImport in
# Grpc.Core expects it: Windows LoadLibrary does not match a loaded module by a different base name.
$native = Join-Path $OutputPath 'grpc_csharp_ext.x64.dll'
if (-not (Test-Path -LiteralPath $native)) {
    throw "Missing native build output: $native"
}
Copy-Item -LiteralPath $native -Destination (Join-Path $targetPath 'Native\grpc_csharp_ext.dll') -Force

Write-Host 'RimWorld adapter installed.'
Get-ChildItem -LiteralPath $targetPath -Recurse -File |
    ForEach-Object { '  {0}  ({1} KB)' -f $_.FullName.Replace($targetPath, ''), [math]::Round($_.Length / 1KB, 1) }
