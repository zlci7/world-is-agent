<#
.SYNOPSIS
Start the development Runtime. Choose its game in the local Console.
.EXAMPLE
.\scripts\start-runtime.ps1
.EXAMPLE
.\scripts\start-runtime.ps1 -DataRoot 'D:\wia-data'
#>
param(
    [string]$AgentConfig,
    [string]$DataRoot
)

$ErrorActionPreference = 'Stop'
$root = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
Get-Command go -ErrorAction Stop | Out-Null

$configPath = $null
if ($AgentConfig) {
    $configPath = $AgentConfig
    if (-not [System.IO.Path]::IsPathRooted($configPath)) { $configPath = Join-Path $root $configPath }
    if (-not (Test-Path -LiteralPath $configPath -PathType Leaf)) { throw "Agent configuration file not found: $configPath" }
    $configPath = (Resolve-Path -LiteralPath $configPath).Path
    $config = Get-Content -LiteralPath $configPath -Raw -Encoding UTF8 | ConvertFrom-Json
    if ($null -eq $config -or $config -isnot [PSCustomObject]) { throw "Agent configuration must be a JSON object: $configPath" }
}
if (-not $DataRoot) {
    $DataRoot = $env:WIA_DATA_ROOT
    if (-not $DataRoot) { $DataRoot = Join-Path $root 'runtime/.local/runtime-data' }
}
if (-not [System.IO.Path]::IsPathRooted($DataRoot)) { $DataRoot = Join-Path $root $DataRoot }
$DataRoot = [System.IO.Path]::GetFullPath($DataRoot)
Write-Host "Data root: $DataRoot"
Write-Host 'Choose a game in the Console. Press Ctrl+C to stop.'

$previousConfig = $env:GAMEAGENT_AGENT_CONFIG
$previousDataRoot = $env:WIA_DATA_ROOT
Push-Location $root
try {
    if ($configPath) { $env:GAMEAGENT_AGENT_CONFIG = $configPath }
    $env:WIA_DATA_ROOT = $DataRoot
    & go run ./runtime/cmd/server
    if ($LASTEXITCODE -ne 0) { throw "Runtime exited with code $LASTEXITCODE" }
}
finally {
    $env:GAMEAGENT_AGENT_CONFIG = $previousConfig
    $env:WIA_DATA_ROOT = $previousDataRoot
    Pop-Location
}
