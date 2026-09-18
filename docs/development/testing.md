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

## Stardew Adapter Tests

```powershell
dotnet run --project adapters/stardew/tests/ProtocolMapper.Tests/ProtocolMapper.Tests.csproj
dotnet run --project adapters/stardew/tests/ActionCancellationRegistry.Tests/ActionCancellationRegistry.Tests.csproj
dotnet run --project adapters/stardew/tests/PlayerInteractProbe.Tests/PlayerInteractProbe.Tests.csproj
```

Adapter context static check:

```powershell
powershell -ExecutionPolicy Bypass -File adapters/stardew/tests/check-context-static.ps1
```

## Stardew Adapter Build

For a custom Stardew install path:

```powershell
$gamePath = "D:\SteamLibrary\steamapps\common\Stardew Valley"
dotnet build adapters/stardew/GameAgent.Stardew.csproj `
  --configuration Debug `
  -p:GamePath="$gamePath"
```

The install helper can build and install when the project default `GamePath` resolves:

```powershell
powershell -ExecutionPolicy Bypass -File scripts/install-stardew-adapter.ps1 `
  -GamePath "$gamePath" `
  -ProjectPath adapters/stardew/GameAgent.Stardew.csproj
```

The helper takes `-ProjectPath`, `-GamePath`, `-OutputPath`, and `-ModsPath`; it assumes nothing about the surrounding repository layout. `-GamePath` controls both the build-time game references and the install target.

Both `GamePath` and `WIA_PROTOCOL_DIR` resolve from an explicit `-p:` parameter or the environment variable of the same name, and fall back to a repository-relative path for local development only. A machine whose Stardew install or protocol checkout is elsewhere must set them explicitly.

To verify the adapter builds without this repository's layout:

```powershell
powershell -ExecutionPolicy Bypass -File adapters/stardew/tests/check-standalone-build.ps1 `
  -GamePath "$gamePath"
```

The script copies the adapter and protocol into a temporary directory outside the repository, builds there with an explicitly supplied protocol path, and then runs a negative probe that must fail. See [logical-separation.md](logical-separation.md).

## Manual Stardew Smoke Test

1. Start the Runtime:

   ```powershell
   .\scripts\start-runtime.ps1
   ```

   The Runtime resolves every path it owns from a data root, so the working
   directory no longer matters. The development script points that root at
   `runtime/`; to do it by hand:

   ```powershell
   $env:WIA_DATA_ROOT = "$PWD/runtime"; go run ./runtime/cmd/server
   ```

   Without a model configuration the Runtime still starts and logs
   `agent core is not ready (needs_configuration)` with the path it expects.

   The Runtime also starts the local client and opens a browser tab at
   `http://127.0.0.1:<port>/#token=...`. Pass `--no-open` to suppress the browser,
   which is what automated runs use; the log then prints the URL to open by hand.

2. Build and install the Stardew adapter.
3. Launch Stardew Valley through `StardewModdingAPI.exe`.
4. Load a save with at least one reachable villager NPC.
5. Interact with an NPC.
6. Confirm SMAPI logs show Runtime connection, `GameEvent`, `EventAck`, `Observation`, `ActionRequest`, `ActionResult`, and `TurnCompletion`.
7. Confirm `runtime/data/traces.jsonl` contains the matching AgentTurn trace.
8. Confirm the browser tab lists that turn.

For dialogue validation, confirm the NPC line appears through Stardew's native dialogue flow, then reply choices or free text appear afterward.

## Architecture Check

```powershell
powershell -ExecutionPolicy Bypass -File scripts/check-architecture.ps1
```

This is a local architecture guardrail for dependency and naming drift. Treat failures in docs or test fixtures as review signals until the check is promoted into a stricter CI gate.

## Documentation

For documentation-only changes:

```powershell
git diff --check
```

Also verify touched Markdown links manually or with a local link checker if available.
