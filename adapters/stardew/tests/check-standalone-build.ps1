# Verifies that the Stardew adapter builds independently of this repository's layout.
#
# The adapter and the protocol are copied into a temporary directory outside the
# repository. The build runs there with an explicitly supplied WIA_PROTOCOL_DIR, so
# the repository-relative fallback inside the project file is unreachable. A negative
# probe then proves the property is really honored instead of silently falling back.
param(
    [Parameter(Mandatory = $true)]
    [string]$GamePath,

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

if (-not (Test-Path -LiteralPath $GamePath)) {
    throw "Stardew install path not found: $GamePath"
}
$GamePath = (Resolve-Path -LiteralPath $GamePath).Path

$protoFile = Join-Path $ProtocolDir 'proto\gameagent.proto'
if (-not (Test-Path -LiteralPath $protoFile)) {
    throw "Protocol source not found: $protoFile"
}

$tempRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("wia-standalone-" + [guid]::NewGuid().ToString('N').Substring(0, 8))
$repoPrefix = $repoRoot.TrimEnd('\') + '\'
if ($tempRoot.StartsWith($repoPrefix, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw "Temporary root must be outside the repository: $tempRoot"
}

$protocolCopy = (Join-Path $tempRoot 'protocol')
$adapterCopy = Join-Path $tempRoot 'adapter'
$project = Join-Path $adapterCopy 'GameAgent.Stardew.csproj'

Write-Host "Protocol source : $ProtocolDir"
Write-Host "Adapter source  : $adapterSource"
Write-Host "Temporary root  : $tempRoot"

New-Item -ItemType Directory -Force -Path $adapterCopy | Out-Null
Copy-Item -LiteralPath $ProtocolDir -Destination $protocolCopy -Recurse -Force

# Copy the adapter without build artifacts or nested test projects.
Get-ChildItem -LiteralPath $adapterSource -Force |
    Where-Object { $_.Name -notin @('bin', 'obj', 'tests') } |
    ForEach-Object { Copy-Item -LiteralPath $_.FullName -Destination $adapterCopy -Recurse -Force }

if (-not (Test-Path -LiteralPath $project)) {
    throw "Copied adapter project not found: $project"
}

$buildLog = Join-Path $tempRoot 'build.log'
$probeLog = Join-Path $tempRoot 'probe.log'

# Set the dependency paths for the build and restore the caller's values afterwards.
$previousProtocolDir = $env:WIA_PROTOCOL_DIR
$previousGamePath = $env:GamePath

try {
    Push-Location $adapterCopy

    Write-Host ''
    Write-Host '== Positive check: build with an explicit protocol directory =='
    $env:WIA_PROTOCOL_DIR = $protocolCopy
    $env:GamePath = $GamePath
    dotnet build $project --configuration $Configuration *> $buildLog
    $buildExit = $LASTEXITCODE
    Get-Content -LiteralPath $buildLog -Encoding UTF8 | Select-Object -Last 8

    if ($buildExit -ne 0) {
        throw "Standalone build failed with exit code $buildExit. See $buildLog"
    }

    $builtDll = Join-Path $adapterCopy "bin\$Configuration\GameAgent.Stardew.dll"
    if (-not (Test-Path -LiteralPath $builtDll)) {
        throw "Standalone build produced no adapter assembly: $builtDll"
    }
    Write-Host "PASS: standalone build produced the adapter assembly"

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
    $env:GamePath = $previousGamePath
    if ($KeepTemp) {
        Write-Host "Temporary directory kept: $tempRoot"
    }
    else {
        Remove-Item -LiteralPath $tempRoot -Recurse -Force -ErrorAction SilentlyContinue
    }
}
