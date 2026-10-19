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

## Appointment Recovery and Console

The Chinese **预约任务** page reads the current Runtime Task database. It cannot create, cancel, retry, complete or reset tasks. An offline world retains its saved records; a connected stream alone does not prove world readiness. RimWorld currently reports appointments as disabled.

Automated coverage uses real SQLite, fake game/model capabilities, the Protocol stream, and an independently exiting test process. Run:

```powershell
go test ./runtime/internal/task ./runtime/internal/gateway ./runtime/internal/bootstrap ./runtime/internal/httpapi -count=1
npm --prefix console/web run type-check
npm --prefix console/web run test:ordering
npm --prefix console/web run build
```

The read-only routes are `GET /api/task-worlds`, `GET /api/tasks` and `GET /api/tasks/detail`. Every request requires the existing local session plus the current `instance_id` and `game_id` from `/api/status`. Lists also require `world_id` and `collection=active|terminal`; details require `world_id`, `entity_id` and `task_id`. A list accepts an optional state, cursor and limit (default 20, maximum 50). Revisions, generations and ticks are decimal strings. Old instance and checkpoint cursors return conflicts; query failure is not an empty success. Task head, counts and page share a read transaction. Live world readiness is independently corroborated and conservatively unconfirmed while the execution lock is busy.

For focused live-game acceptance, use a copied test save and an isolated data root with Runtime 0.2.0 and Stardew Adapter 0.1.1:

1. Make an appointment on a validated route. Confirm one Task appears and remains after refreshing or closing the browser.
2. Save normally, leave the game, and load the save. Confirm the same appointment resumes from that exact checkpoint after world binding.
3. Restart the Runtime while the game remains open, first while waiting and then during travel. Confirm reconnection and evidence reconciliation, or an explicit pause when execution cannot be confirmed.
4. Save and reconnect after arrival while waiting for the player. Confirm a single meeting result. Repeat without the player and confirm a single expiry result.
5. Load a save from before Task creation, then one from before execution. Confirm absent future tasks and results in the page, current Task Context and subsequent dialogue. Memory's independent branch-isolation limit still applies.
6. Exercise a lost checkpoint Finish, invalid reference and old callback. Confirm bounded saving, explicit recovery blocking, and no duplicate valid wake or stale control release. Never edit a production save or database to run a failure probe.
7. Inspect NPC position, control release or legal handoff, and resumed native schedule alongside Task state. Completion alone does not prove cleanup.
8. In the console, test both game-switch directions, same-game model replacement, offline/empty/paused states, status filtering, mobile details/back, and keyboard focus. Check 1440px, 1024px and 390px widths. Old requests must not populate the new scope.

If recovery cannot be established, preserve the isolated save, database and logs. Keep the world unready or the Task paused; do not reset the task collection, invent success, or replay uncertain game effects. See the [Phase11.1 acceptance record](../phase11/phase11.1-acceptance.md) for current evidence. Automated and browser checks do not replace these game checks.

## Hosted Invitation Beta

Build the embedded account website before running hosted integration checks:

```powershell
npm --prefix console/web run type-check
npm --prefix console/web run test:ordering
npm --prefix console/web run build
go test ./runtime/internal/accounts ./runtime/internal/hosted ./runtime/internal/workerapi ./runtime/internal/context ./runtime/internal/llm/deepseek -count=1
```

The hosted suite uses real SQLite, a certificate-verified local TLS gRPC gateway, separate bootstrap Runtime instances and stub model endpoints. It covers session/CSRF boundaries, key binding/revocation, identical game/world/NPC identities across accounts, worker reconstruction, conservative quota reservations and cancellation. Docker allocation tests use a fake engine; they do not prove that a container launches on Linux.

Management tests additionally cover independent administrator/player session scopes, version-1 to version-2 migration and backups, atomic personal/global quota admission, inherited/overridden limits, audit rollback, unknown usage metadata, query filters, CSV formula neutralization and oversized-export rejection. Browser preview serves `/admin/login` with `operator@example.test` and the existing dummy password; it seeds settled, failed and uncertain calls. These are isolated fixtures, not production credentials or actual model usage. See [administration](hosted-administration.md).

Email/account tests cover schema migration with backup, SMTP activation, expiry/resend/wrong-attempt persistence, atomic invitation/code consumption, reset replay and credential revocation. Local SMTP receivers exercise implicit TLS and STARTTLS with a trusted test CA, rejection of untrusted certificates and cancellation. Administrator tests cover current-role checks, CSRF, invitation revocation, account availability, audit attribution and read-only worker inspection.

An opt-in `TestHostedBrowserPreview` fixture serves the built account website and isolated local workers. Set `WIA_HOSTED_BROWSER_PREVIEW=1`; its output includes a trusted-localhost URL and a temporary invitation. `WIA_HOSTED_PREVIEW_STOP_FILE` ends the fixture when the file appears. Optional `WIA_HOSTED_PREVIEW_FAULT_FILE` lists exact GET paths to return 503 for failure/retry checks. Preview accounts and the seeded offline Task world are synthetic and are removed with the test directory.

The preview seeds `operator@example.test`, `player@example.test` and an unverified `legacy@example.test`, using the fixture password `a long test password`. Only the first has an administrator role. Its loopback-only `/__preview/mail` route reads the fake outbox for browser verification/reset checks. This route and sender exist only in test code; they are not production mail configuration. Tests must not send to real recipients.

Real-host acceptance uses the [hosted deployment guide](../../deploy/hosted/README.md): verify public gRPC TLS, a clean player installation, two-account isolation, save/load and network/worker recovery, a complete closed-volume restore, and capacity on the chosen host/model. The [acceptance record](../phase11/phase11.2-acceptance.md) separates local evidence from those pending checks.

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
