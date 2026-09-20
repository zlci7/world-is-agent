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

## Live Settings Real-Game Acceptance

The user reported the Phase10.4/10.5 real-game baseline passed. The following checks validate the live-settings build. Use Runtime 0.2.0, Stardew Adapter 0.1.1, and the existing RimWorld Adapter with a dedicated data root. Record Runtime and adapter logs plus `data/traces.jsonl`.

1. Choose Stardew Valley, configure a model and wait for Ready. Start Stardew and complete one dialogue turn. Confirm Current Game and the live connection match.
2. While a turn is active, switch to RimWorld. Confirm that the turn is canceled, its recorded history remains, the Runtime PID and console URL stay unchanged, and Ready reports RimWorld without a restart prompt.
3. Start RimWorld and complete a colonist dialogue. Switch back to Stardew while both games remain open. The matching Stardew adapter reconnects automatically and can complete another turn; the other game is rejected with `game_mismatch`.
4. Open Model settings while Ready. Confirm the existing provider and model are shown, no key is filled, and Cancel leaves the configuration unchanged. Enter the new model, key and any custom base URL. Save; the Runtime checks the candidate, replaces the active instance, and the adapter reconnects. Confirm the next dialogue uses the new model.
5. Submit an invalid key. Confirm a readable error, the form remains editable, and the old model can still complete a dialogue. Closing the form clears the typed key.
6. Refresh the console and restart the Runtime once. Confirm the selected game and model persist, and the key is absent from browser-visible status and traces.
7. Connect before Ready and during reconfiguration: neither admits game work. Matching adapters reconnect after Ready. Same-game streams remain separate EnvironmentSessions; this does not guarantee concurrent writes to the same save.

Automatic retry repeats the Protocol bootstrap and world binding; it does not replay an interrupted ordinary turn. Actual game dialogue and action reset require these focused real-game checks in addition to automated lifecycle tests.

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
