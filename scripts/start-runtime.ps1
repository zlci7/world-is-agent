<#
.SYNOPSIS
Start the development Runtime with a selected Agent configuration (requires Go).
.EXAMPLE
.\scripts\start-runtime.ps1
.EXAMPLE
.\scripts\start-runtime.ps1 -AgentConfig 'runtime/config/agent.json'
#>
param(
    # Edit this default to choose the configuration used without arguments.
    # Relative configuration paths are resolved from the repository root.
    [ValidateNotNullOrEmpty()]
    [string]$AgentConfig = 'runtime/config/games/stardew-valley/agent.json'
)

$ErrorActionPreference = 'Stop'
$root = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$configPath = $AgentConfig
if (-not [System.IO.Path]::IsPathRooted($configPath)) {
    $configPath = Join-Path $root $configPath
}
if (-not (Test-Path -LiteralPath $configPath -PathType Leaf)) {
    throw "Agent configuration file not found: $configPath"
}
$configPath = (Resolve-Path -LiteralPath $configPath).Path
$config = Get-Content -LiteralPath $configPath -Raw -Encoding UTF8 | ConvertFrom-Json
if ($null -eq $config -or $config -isnot [PSCustomObject]) {
    throw "Agent configuration must be a JSON object: $configPath"
}
Get-Command go -ErrorAction Stop | Out-Null

Write-Host "Agent configuration: $configPath"
$dataRoot = Join-Path $root 'runtime'
Write-Host "Data root: $dataRoot"
foreach ($field in @('async_action_timeout_ms', 'turn_timeout_ms')) {
    $value = $config.$field
    if ($null -eq $value -or $value -le 0) { $value = 'Runtime default (not explicitly configured)' }
    Write-Host "${field}: $value"
}
Write-Host 'Starting Runtime in foreground. Press Ctrl+C to stop.'

$previousConfig = $env:GAMEAGENT_AGENT_CONFIG
$previousDataRoot = $env:WIA_DATA_ROOT
Push-Location $root
try {
    $env:GAMEAGENT_AGENT_CONFIG = $configPath
    # Development keeps its data in the repository instead of the platform data
    # directory, so the config tree under runtime/ stays the authority.
    $env:WIA_DATA_ROOT = $dataRoot
    & go run ./runtime/cmd/server
    if ($LASTEXITCODE -ne 0) {
        throw "Runtime exited with code $LASTEXITCODE"
    }
}
finally {
    $env:GAMEAGENT_AGENT_CONFIG = $previousConfig
    $env:WIA_DATA_ROOT = $previousDataRoot
    Pop-Location
}
