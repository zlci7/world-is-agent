# Verifies that the RimWorld adapter builds independently of this repository's layout,
# and that it needs no game installation at all.
#
# Unlike the Stardew adapter, this project references the game only through Krafs.Rimworld.Ref
# reference assemblies, so there is no -GamePath parameter: the absence of a game path is the
# property under test, not a convenience.
#
# The adapter and the protocol are copied outside the repository and built there with an explicitly
# supplied WIA_PROTOCOL_DIR, so the repository-relative fallback inside the project file is
# unreachable. A negative probe proves the property is honored instead of silently falling back.
param(
    [string]$ProtocolDir,
    [string]$Configuration = 'Debug',
    [switch]$KeepTemp
)

$ErrorActionPreference = 'Stop'

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..\..\..')).Path
$adapterSource = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path

if (-not $ProtocolDir) {
    $ProtocolDir = Join-Path $repoRoot 'protocol'
}
$ProtocolDir = (Resolve-Path -LiteralPath $ProtocolDir).Path

$protoFile = Join-Path $ProtocolDir 'proto\gameagent.proto'
if (-not (Test-Path -LiteralPath $protoFile)) {
    throw "Protocol source not found: $protoFile"
}

$tempRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("wia-rimworld-standalone-" + [guid]::NewGuid().ToString('N').Substring(0, 8))
$repoPrefix = $repoRoot.TrimEnd('\') + '\'
if ($tempRoot.StartsWith($repoPrefix, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw "Temporary root must be outside the repository: $tempRoot"
}

$protocolCopy = Join-Path $tempRoot 'protocol'
$adapterCopy = Join-Path $tempRoot 'adapter'
$project = Join-Path $adapterCopy 'WiaRimWorld.csproj'
$output = Join-Path $adapterCopy "bin\$Configuration\net472"

Write-Host "Protocol source : $ProtocolDir"
Write-Host "Adapter source  : $adapterSource"
Write-Host "Temporary root  : $tempRoot"

New-Item -ItemType Directory -Force -Path $adapterCopy | Out-Null
Copy-Item -LiteralPath $ProtocolDir -Destination $protocolCopy -Recurse -Force

Get-ChildItem -LiteralPath $adapterSource -Force |
    Where-Object { $_.Name -notin @('bin', 'obj', 'tests') } |
    ForEach-Object { Copy-Item -LiteralPath $_.FullName -Destination $adapterCopy -Recurse -Force }

if (-not (Test-Path -LiteralPath $project)) {
    throw "Copied adapter project not found: $project"
}

$buildLog = Join-Path $tempRoot 'build.log'
$probeLog = Join-Path $tempRoot 'probe.log'
$previousProtocolDir = $env:WIA_PROTOCOL_DIR

try {
    Push-Location $adapterCopy

    Write-Host ''
    Write-Host '== Positive check: build outside the repository with an explicit protocol directory =='
    $env:WIA_PROTOCOL_DIR = $protocolCopy
    dotnet build $project --configuration $Configuration *> $buildLog
    $buildExit = $LASTEXITCODE
    Get-Content -LiteralPath $buildLog -Encoding UTF8 | Select-Object -Last 8

    if ($buildExit -ne 0) {
        throw "Standalone build failed with exit code $buildExit. See $buildLog"
    }

    $builtDll = Join-Path $output 'WiaRimWorld.dll'
    if (-not (Test-Path -LiteralPath $builtDll)) {
        throw "Standalone build produced no adapter assembly: $builtDll"
    }
    Write-Host 'PASS: standalone build produced the adapter assembly'

    Write-Host ''
    Write-Host '== Property check: no game binary is required or redistributed =='
    $leaked = Get-ChildItem -LiteralPath $output -File |
        Where-Object { $_.Name -match '^(Assembly-CSharp|UnityEngine|Unity\.)' }
    if ($leaked) {
        throw "Build output contains game binaries: $($leaked.Name -join ', ')"
    }
    Write-Host 'PASS: the build output contains no Ludeon or Unity assembly'

    Write-Host ''
    Write-Host '== Negative probe: an unusable protocol directory must fail the build =='
    $env:WIA_PROTOCOL_DIR = Join-Path $tempRoot 'nonexistent-protocol'
    dotnet build $project --configuration $Configuration *> $probeLog
    $probeExit = $LASTEXITCODE

    if ($probeExit -eq 0) {
        throw "WIA_PROTOCOL_DIR was ignored: the build succeeded with an unusable protocol directory. Check whether the project falls back to a repository-relative path."
    }
    if (-not (Select-String -LiteralPath $probeLog -Pattern 'nonexistent-protocol' -Quiet -Encoding UTF8)) {
        throw "The negative probe failed for an unrelated reason, so it does not prove WIA_PROTOCOL_DIR is used. See $probeLog"
    }
    Write-Host 'PASS: the build honors WIA_PROTOCOL_DIR'

    Write-Host ''
    Write-Host 'Standalone build verification passed.'
}
finally {
    Pop-Location
    $env:WIA_PROTOCOL_DIR = $previousProtocolDir
    if ($KeepTemp) {
        Write-Host "Temporary directory kept: $tempRoot"
    }
    else {
        Remove-Item -LiteralPath $tempRoot -Recurse -Force -ErrorAction SilentlyContinue
    }
}
