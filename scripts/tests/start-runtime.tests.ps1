$ErrorActionPreference = 'Stop'
$root = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
$launcher = Join-Path $root 'scripts/start-runtime.ps1'
if (-not (Test-Path -LiteralPath $launcher)) { throw 'Runtime launcher is missing' }

# Intercept only the external server process; configuration and path handling run normally.
function go {
    $launchTest.Invocation = @{
        Arguments = @($args)
        Directory = (Get-Location).Path
        Config = $env:GAMEAGENT_AGENT_CONFIG
    }
    $global:LASTEXITCODE = $launchTest.ExitCode
}
function Assert($condition, $message) {
    if (-not $condition) { throw $message }
}

$previousConfig = $env:GAMEAGENT_AGENT_CONFIG
$launchTest = @{ ExitCode = 0; Invocation = $null }
Push-Location $env:TEMP
try {
    $env:GAMEAGENT_AGENT_CONFIG = 'existing-config'
    $callerDirectory = (Get-Location).Path
    foreach ($config in @('runtime/config/games/stardew-valley/agent.json', 'runtime/config/agent.json')) {
        $launchTest.Invocation = $null
        if ($config -like '*stardew-valley*') { & $launcher }
        else { & $launcher -AgentConfig $config }
        Assert ($launchTest.Invocation.Config -eq (Join-Path $root $config)) 'Wrong configuration supplied to Runtime'
        Assert ($launchTest.Invocation.Directory -eq $root) 'Runtime must start at repository root'
        Assert (($launchTest.Invocation.Arguments -join ' ') -eq 'run ./runtime/cmd/server') 'Wrong Go command'
        Assert ($env:GAMEAGENT_AGENT_CONFIG -eq 'existing-config') 'Caller environment was changed'
        Assert ((Get-Location).Path -eq $callerDirectory) 'Caller directory was changed'
    }
    & $launcher -AgentConfig (Join-Path $root 'runtime/config/agent.json')
    Assert ($launchTest.Invocation.Config -eq (Join-Path $root 'runtime/config/agent.json')) 'Absolute path was not accepted'

    foreach ($invalid in @('runtime/config/nonexistent-launcher-test.json', 'AGENTS.md', 'runtime/config')) {
        $launchTest.Invocation = $null
        $failed = $false
        try { & $launcher -AgentConfig $invalid } catch { $failed = $true }
        Assert $failed 'Invalid configuration must fail'
        Assert ($null -eq $launchTest.Invocation) 'Invalid configuration started the server'
    }

    $launchTest.ExitCode = 7
    $failed = $false
    try { & $launcher } catch { $failed = $_.Exception.Message -match '7' }
    Assert $failed 'Server failure must be reported'
    Assert ($env:GAMEAGENT_AGENT_CONFIG -eq 'existing-config') 'Failure leaked environment changes'
    Assert ((Get-Location).Path -eq $callerDirectory) 'Failure leaked directory changes'
    Write-Host 'PASS: default/relative/absolute configuration, working directory, validation, cleanup and server failure'
}
finally {
    Pop-Location
    $env:GAMEAGENT_AGENT_CONFIG = $previousConfig
}
