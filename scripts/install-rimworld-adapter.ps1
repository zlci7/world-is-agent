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
# It is only needed to resolve the install target, and it is never guessed: a wrong guess installs
# into a different game directory and still reports success.
if (-not $GamePath) { $GamePath = $env:WIA_RIMWORLD_GAME_PATH }

if (-not (Test-Path -LiteralPath $ProjectPath)) {
    throw "RimWorld adapter project not found: $ProjectPath"
}

$projectDirectory = Split-Path -Parent $ProjectPath
if (-not $OutputPath) {
    $OutputPath = Join-Path $projectDirectory "bin\$Configuration\net472"
}

if (-not $ModsPath) {
    if (-not $GamePath) {
        throw 'Game path not specified. Pass -GamePath or set WIA_RIMWORLD_GAME_PATH.'
    }
    if (-not (Test-Path -LiteralPath $GamePath)) {
        throw "RimWorld install path not found: $GamePath"
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

# Preflight, before anything is removed. Staging empties the target's assembly folders, so a missing
# build output discovered afterwards would leave a half-installed mod behind: the old assemblies
# already deleted, the new ones never copied.
$aboutSource = Join-Path $projectDirectory 'About\About.xml'
if (-not (Test-Path -LiteralPath $aboutSource)) {
    throw "Missing mod metadata: $aboutSource"
}

# The XML patch is what attaches the WIA entry point to the Human def. Without it the adapter loads
# and handshakes but no gizmo ever appears, which looks like a broken mod rather than a missing file.
$patchesSource = Join-Path $projectDirectory 'Patches'
if (-not (Test-Path -LiteralPath $patchesSource)) {
    throw "Missing mod patches: $patchesSource"
}

$patchFiles = @(Get-ChildItem -LiteralPath $patchesSource -File -Filter '*.xml')
if ($patchFiles.Count -eq 0) {
    throw "No patch XML found in: $patchesSource"
}

foreach ($name in $managed) {
    $source = Join-Path $OutputPath $name
    if (-not (Test-Path -LiteralPath $source)) {
        throw "Missing build output: $source"
    }
}

# The native library must sit outside Assemblies/, and must be named exactly as the DllImport in
# Grpc.Core expects it: Windows LoadLibrary does not match a loaded module by a different base name.
$nativeSource = Join-Path $OutputPath 'grpc_csharp_ext.x64.dll'
if (-not (Test-Path -LiteralPath $nativeSource)) {
    throw "Missing native build output: $nativeSource"
}

# RimWorld keeps every staged *.dll open for as long as it runs, and staging deletes them. Refusing
# here is what keeps a locked file from becoming a half-installed mod.
$running = @(Get-Process -Name 'RimWorldWin64' -ErrorAction SilentlyContinue)
if ($running.Count -gt 0) {
    $runningIds = ($running | ForEach-Object { $_.Id }) -join ', '
    throw "RimWorld is running (pid $runningIds). Close the game before installing: it holds the staged assemblies open."
}

Write-Host "Installing adapter to: $targetPath"

# Staging is a whitelist, not a merge. The installed tree is what RimWorld loads, so a managed
# assembly left behind by an earlier version would silently change the mod's behaviour: it stays a
# real part of the loaded program, and an unloadable one in this folder can stop later assemblies
# from loading at all. Patches/ is emptied for the same reason - a leftover patch XML would keep
# modifying defs after the code that relied on it is gone. Anything else the user keeps in the mod
# folder is left alone.
foreach ($folder in @('Assemblies', 'Native', 'Patches')) {
    $folderPath = Join-Path $targetPath $folder
    if (Test-Path -LiteralPath $folderPath) {
        Remove-Item -LiteralPath $folderPath -Recurse -Force
    }

    New-Item -ItemType Directory -Force -Path $folderPath | Out-Null
}

New-Item -ItemType Directory -Force -Path (Join-Path $targetPath 'About') | Out-Null
Copy-Item -LiteralPath $aboutSource -Destination (Join-Path $targetPath 'About\About.xml') -Force

foreach ($patch in $patchFiles) {
    Copy-Item -LiteralPath $patch.FullName -Destination (Join-Path $targetPath "Patches\$($patch.Name)") -Force
}

foreach ($name in $managed) {
    Copy-Item -LiteralPath (Join-Path $OutputPath $name) -Destination (Join-Path $targetPath "Assemblies\$name") -Force
}

Copy-Item -LiteralPath $nativeSource -Destination (Join-Path $targetPath 'Native\grpc_csharp_ext.dll') -Force

Write-Host 'RimWorld adapter installed.'
Get-ChildItem -LiteralPath $targetPath -Recurse -File |
    ForEach-Object { '  {0}  ({1} KB)' -f $_.FullName.Replace($targetPath, ''), [math]::Round($_.Length / 1KB, 1) }
