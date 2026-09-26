<#
.SYNOPSIS
Start the Phase12 local Runtime.

.DESCRIPTION
The default path starts the Runtime with the existing embedded client build.
Use -Rebuild when the web client needs to be reconstructed before startup.

.EXAMPLE
.\scripts\start-phase12.ps1

.EXAMPLE
.\scripts\start-phase12.ps1 -Rebuild

.EXAMPLE
.\scripts\start-phase12.ps1 -True -NoOpen -DataRoot 'D:\wia-data'
#>
param(
    [Alias('Build', 'True')]
    [switch]$Rebuild,
    [switch]$NoOpen,
    [string]$DataRoot,
    [string]$ModelConfig,
    [string]$HttpAddr = '127.0.0.1:0'
)

$ErrorActionPreference = 'Stop'
$root = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$webRoot = Join-Path $root 'console\web'
$embeddedIndex = Join-Path $root 'console\dist\index.html'

Get-Command go -ErrorAction Stop | Out-Null

if ($Rebuild) {
    Get-Command npm -ErrorAction Stop | Out-Null
    Write-Host 'Installing web client dependencies...'
    Push-Location $webRoot
    try {
        & npm ci
        if ($LASTEXITCODE -ne 0) { throw "npm ci failed with exit code $LASTEXITCODE" }

        Write-Host 'Building web client...'
        & npm run build
        if ($LASTEXITCODE -ne 0) { throw "npm run build failed with exit code $LASTEXITCODE" }
    }
    finally {
        Pop-Location
    }
}
elseif (-not (Test-Path -LiteralPath $embeddedIndex -PathType Leaf)) {
    throw "Embedded web client is missing. Run .\scripts\start-phase12.ps1 -Rebuild first."
}

function Resolve-OptionalPath([string]$value, [string]$label) {
    if (-not $value) { return $null }
    $path = $value
    if (-not [System.IO.Path]::IsPathRooted($path)) { $path = Join-Path $root $path }
    return [System.IO.Path]::GetFullPath($path)
}

$runtimeArgs = @('-http-addr', $HttpAddr)
if ($NoOpen) { $runtimeArgs += '-no-open' }

$resolvedDataRoot = Resolve-OptionalPath $DataRoot 'data root'
if ($resolvedDataRoot) { $runtimeArgs += @('-data-root', $resolvedDataRoot) }

$resolvedModelConfig = Resolve-OptionalPath $ModelConfig 'model config'
if ($resolvedModelConfig) {
    if (-not (Test-Path -LiteralPath $resolvedModelConfig -PathType Leaf)) {
        throw "Model configuration file not found: $resolvedModelConfig"
    }
    $runtimeArgs += @('-model-config', $resolvedModelConfig)
}

Write-Host 'Starting Phase12 Runtime. Press Ctrl+C to stop.'
Push-Location $root
try {
    & go run ./runtime/cmd/server @runtimeArgs
    if ($LASTEXITCODE -ne 0) { throw "Runtime exited with code $LASTEXITCODE" }
}
finally {
    Pop-Location
}
