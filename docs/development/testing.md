# Testing

This guide lists the checks used during local development.

Run commands from the repository root unless noted otherwise.

For development principles, documentation expectations, and protocol or adapter change rules, see [guide.md](guide.md).

## Go Runtime

```powershell
go test ./runtime/... ./protocol/gen/go/...
```

Race detector:

```powershell
go test -race ./runtime/...
```

On Windows, the race detector requires a working 64-bit C toolchain. Run it on Linux, CI, or a Windows machine with the required compiler installed.

## Local Client

The client is built from `console/web` and embedded into the Runtime binary. Build it before running a Runtime whose UI you want to see:

```powershell
cd console/web
npm ci
npm run build
npm run type-check
```

`go build ./...` does not need Node: without a build, the Runtime serves `assets_not_built` with these commands instead of an empty page.

## Protocol

```powershell
powershell -ExecutionPolicy Bypass -File protocol/tests/check-protocol-static.ps1
powershell -ExecutionPolicy Bypass -File protocol/tests/check-go-generation.ps1
```

Run these after editing `protocol/proto/gameagent.proto` or generated bindings.

## Official Adapter Verification

Official adapters are tested from the independent local `world-is-agent-adapters` repository. The full commands and game-specific dependencies are maintained in each game's README; both repositories may be anywhere on disk. As described in [Official Adapters](guide.md#official-adapters), each test receives a Protocol directory exported from that game's pinned tag rather than the Runtime working tree.

```powershell
$adapterRoot = 'D:\src\world-is-agent-adapters'
$runtimeRoot = 'D:\src\world-is-agent'
$gamePath = 'D:\SteamLibrary\steamapps\common\Stardew Valley'
$protocolRepository = $runtimeRoot

function Export-PinnedProtocol([string]$gameDirectory) {
  $pin = (Get-Content -Raw -LiteralPath (Join-Path $gameDirectory 'protocol.version')).Trim()
  $resolvedCommit = & git -C $protocolRepository rev-parse --verify "refs/tags/$pin^{commit}"
  if ($LASTEXITCODE -ne 0) { throw "Protocol tag is unavailable: $pin" }
  Write-Host "Protocol $pin resolves to $resolvedCommit"

  $exportRoot = Join-Path $env:TEMP ('wia-protocol-' + [guid]::NewGuid().ToString('N'))
  New-Item -ItemType Directory -Path $exportRoot | Out-Null
  $archive = Join-Path $exportRoot 'protocol.zip'
  git -C $protocolRepository archive --format=zip --output=$archive "refs/tags/$pin" protocol
  if ($LASTEXITCODE -ne 0) { throw "Failed to export Protocol tag: $pin" }
  Expand-Archive -LiteralPath $archive -DestinationPath $exportRoot
  return (Join-Path $exportRoot 'protocol')
}

$stardewDirectory = Join-Path $adapterRoot 'stardew-valley'
$rimworldDirectory = Join-Path $adapterRoot 'rimworld'
$stardewProtocolDir = Export-PinnedProtocol $stardewDirectory
$rimworldProtocolDir = Export-PinnedProtocol $rimworldDirectory

& "$stardewDirectory\scripts\test-adapter.ps1" `
  -GamePath $gamePath `
  -ProtocolRepository $protocolRepository `
  -ProtocolDir $stardewProtocolDir

& "$rimworldDirectory\scripts\test-adapter.ps1" `
  -ProtocolRepository $protocolRepository `
  -ProtocolDir $rimworldProtocolDir

& "$adapterRoot\scripts\check-architecture.ps1"
```

The two exports intentionally use separate variables. If the games pin different Protocol tags, each script still receives the exact contract declared by its own `protocol.version`.

Each game also provides `tests/check-standalone-build.ps1`. It copies only that game directory and the explicit Protocol export to temporary locations, then exercises build, tests, release packaging, temporary installation, and negative dependency, content, tag, and version probes. Stardew requires `-GamePath`; RimWorld builds against its pinned reference package. The completed independent baselines are 494 Stardew tests plus static checks and 42 RimWorld tests plus standalone build, package, temporary-installation, and negative probes.

## Manual Stardew Smoke Test

1. Start the Runtime:

   ```powershell
   .\scripts\start-runtime.ps1
   ```

   Use a dedicated data root:

   ```powershell
   .\scripts\start-runtime.ps1 -DataRoot "$PWD\runtime\.local\stardew-acceptance"
   ```

   Choose **Stardew Valley** in the console, configure the model if required,
   and wait for **Ready**. Startup does not choose a game or prepare missing
   profile files by itself.

   The Runtime also starts the local client and opens a browser tab at
   `http://127.0.0.1:<port>/#token=...`. Pass `--no-open` to suppress the browser,
   which is what automated runs use; the log then prints the URL to open by hand.

2. Build and install the Stardew adapter.
3. Launch Stardew Valley through `StardewModdingAPI.exe`.
4. Load a save with at least one reachable villager NPC.
5. Interact with an NPC.
6. Confirm SMAPI logs show Runtime connection, `GameEvent`, `EventAck`, `Observation`, `ActionRequest`, `ActionResult`, and `TurnCompletion`.
7. Confirm `<data root>/data/traces.jsonl` contains the matching AgentTurn trace.
8. Confirm the browser tab lists that turn.

For dialogue validation, confirm the NPC line appears through Stardew's native dialogue flow, then reply choices or free text appear afterward.

## Joint Phase 10.4/10.5 Real-Game Acceptance

Use one dedicated data root and record status, Runtime logs, adapter logs, and turn traces. This remains manual acceptance; automated tests do not establish it.

1. With an empty root, choose Stardew Valley, configure the model, reach Ready, then start Stardew and complete one turn. Confirm Current Game is Stardew Valley with one connection.
2. While Ready, choose RimWorld. Confirm Next Game is RimWorld and restart is required, while Stardew stays connected and can complete another turn with the loaded Stardew profile.
3. Restart the Runtime, reach Ready, start RimWorld, and complete a colonist dialogue turn. Confirm Current Game is RimWorld and no Next Game is shown.
4. Repeat the switch in the other direction: save Stardew while RimWorld is Ready, confirm RimWorld remains active until restart, restart, and complete a Stardew turn.
5. For each loaded game, connect a second adapter reporting the same `game_id`. Confirm both streams complete bootstrap and the connection count increases. They are separate EnvironmentSessions; this does not guarantee safe concurrent writes to one save.
6. Connect or simulate an adapter for the other game. Confirm `game_mismatch`, no `EnvironmentReady` or capability discovery, no active connection, and no event or task work.
7. Connect before Ready. Confirm `runtime_not_ready` and zero active connections. Once Ready, establish a new connection: RimWorld retries every five seconds; Stardew uses `gameagent_runtime_reconnect` or a game restart.

Use the adapters built and installed from the independent Adapter repository. Keep earlier Stardew and RimWorld observations as dated adapter baselines; they do not replace this final combined acceptance.

## Architecture Check

```powershell
powershell -ExecutionPolicy Bypass -File scripts/check-architecture.ps1
```

This is the main-repository guard for Runtime and Protocol dependency and naming drift. The optional Adapter guard is `world-is-agent-adapters/scripts/check-architecture.ps1`.

## Documentation

For documentation-only changes:

```powershell
git diff --check
```

Also verify touched Markdown links manually or with a local link checker if available.
