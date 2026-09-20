# Development Guide

World Is Agent is experimental. Contributions should keep the Runtime / Protocol / Adapter boundary clear and keep public documentation aligned with implementation.

Run commands from the repository root unless noted otherwise. Check commands live in [testing.md](testing.md).

## Development Principles

- Runtime code stays game-agnostic.
- Protocol changes describe cross-boundary contracts.
- Adapter code owns game-specific APIs, UI, threading, and execution rules.
- Provider code stays behind the runtime model interface.
- Documentation changes should state current behavior clearly.

## Repository Areas

```text
runtime/     Go runtime, gateway, loop, scheduling, memory, trace, providers, local control plane
protocol/    Protobuf contract and generated bindings
console/     Local client (Vue 3 + TypeScript + Vite) and the Go package that embeds its build
docs/        Status, guides, ADRs, phase plans, acceptance records
scripts/     Local validation and helper scripts
```

## Repository Model

WIA is organized as Runtime + Protocol + Adapter across two independent Git repositories. `world-is-agent` owns the Runtime, Console, Protocol, and shipped Game Profiles, prompts, and definitions under `runtime/config/games/`. `world-is-agent-adapters` owns the official game translation and execution implementations. See [logical-separation.md](logical-separation.md).

## Runtime Configuration And Game Selection

```powershell
.\scripts\start-runtime.ps1
.\scripts\start-runtime.ps1 -DataRoot 'D:\wia-data'
```

The script uses `-DataRoot`, then inherited `WIA_DATA_ROOT`, then `runtime/.local/runtime-data`. Startup does not choose a game or prepare missing profile files. Choose a game in the local console; that request validates the embedded assets, prepares missing files under `<data root>/config/games/<game_id>/`, and then writes `<data root>/config/active-game.json`.

The data root must be on a filesystem that supports hard links. Profile preparation uses them to publish complete files atomically without replacing existing user files. Unsupported filesystems fail preparation with `storage_unavailable`; use a data root on a supporting filesystem.

`-AgentConfig` is an explicit development override. A relative argument resolves from the repository root. If the parameter is omitted, the script respects an inherited `GAMEAGENT_AGENT_CONFIG`. Relative paths inside agent configuration continue to resolve from the data root.

One process loads one profile at a time. Selecting another game applies it in the running process: active turns are canceled, recorded history is retained, and the old connections are closed before the replacement instance is admitted. The HTTP and gRPC addresses and browser session remain valid. Model settings are available after setup; each submission requires an API key to be supplied and tests the candidate before applying it. Enter a custom base URL when needed, or leave it empty to use the provider default. A failed probe leaves the current instance unchanged. Game installation paths belong to adapter build and install commands and are not collected by the Runtime console.

## Local Client

The Runtime serves a local HTTP control plane and embeds the client into its own binary, so starting the Runtime opens a browser at `http://127.0.0.1:<port>/#token=...` and hands over a session credential automatically. Nothing is copied by hand, and no route returns the model credential.

The client is built separately and is not committed:

```powershell
cd console/web
npm ci            # installs exactly what console/web/package-lock.json pins
npm run build     # writes console/dist, which console/embed.go embeds
```

Use `npm install` only when you intend to change a dependency and commit the regenerated lock. `npm run build` clears previous build output first: the bundle is embedded into the Runtime binary, so a leftover bundle would become binary size rather than harmless disk usage.

`go build ./...` succeeds without the client, and the Runtime then reports `assets_not_built` with the commands above instead of serving an empty page. Development against the Vite dev server uses `npm run dev`, which proxies `/api` to `WIA_DEV_RUNTIME` (default `http://127.0.0.1:8765`).

Design and acceptance record: [Phase10.2-2 本地控制面](../phase10/GameAgent%20MVP0%20Phase10.2-2%20本地控制面技术开发方案.md).

## Portable Release

The Runtime is distributed as a portable package: extract it anywhere and run the executable with no arguments.

Published versions are immutable. `VERSION = 0.2.0` identifies the live-settings acceptance build; it must not replace the published v0.1.0 archive. Publishing a release follows user acceptance.

```powershell
.\scripts\release-runtime.ps1              # version from VERSION
.\scripts\release-runtime.ps1 -Version 0.2.0
```

The script builds the client first, then builds the Runtime with the version written at link time, and assembles a package that contains only what a user needs:

```text
dist/world-is-agent-v<version>-windows-amd64.zip
├── wia-runtime.exe
├── README.txt
└── LICENSE
```

Two things are deliberate. The package is staged in a temporary directory rather than in the repository, because a build output does not belong in the tree; only the archive lands in `dist/`, which git ignores. And the game adapter is not included: adapters are C# projects that compile against a local game installation, and they are installed separately as mods, so shipping one would ship something most users cannot build or verify. `deploy/release/README.txt` is the file users read, and it says what the Runtime is, how to run it, that an adapter is needed before there is anything to show, and where the data root and the credential live.

Verification is a run from outside the repository, not a unit test:

```powershell
Expand-Archive dist/world-is-agent-v0.2.0-windows-amd64.zip -DestinationPath $env:TEMP\wia-check
& "$env:TEMP\wia-check\world-is-agent-v0.2.0-windows-amd64\wia-runtime.exe" --data-root $env:TEMP\wia-check-root
```

The binary must expose both embedded Game Profiles, serve the embedded client, and report the injected version on `/api/status` without any repository file, Node.js or Go on the path. A game-selection request must prepare the selected assets before the Runtime can reach Ready. `--data-root` keeps the check away from the data root a user already has; without it the Runtime uses the platform data directory.

## Adapter Capabilities From Third-Party Mods

Adapters may wrap another mod's API and expose it as a Capability, so the agent can select it like any other tool. Third-party mods stay optional dependencies: when one is absent, the adapter still loads and the capability is rejected with an explicit code.

The normative rules — optional dependency, public-interface access, main-thread execution, external text validation, direct execution without player confirmation, and the landing checklist — live in [mod-capability-integration.md](mod-capability-integration.md). The first integration built on them is [Phase10.1 邮件能力方案](../phase10/GameAgent%20MVP0%20Phase10.1%20Mod%20邮件能力技术开发方案.md).

## Official Adapters

The official adapters are maintained at [world-is-agent-adapter](https://github.com/zlci7/world-is-agent-adapter). Clone it into the local `world-is-agent-adapters` directory, or choose another location and set it explicitly; scripts do not assume that the two repositories are siblings.

```powershell
$adapterRoot = 'D:\src\world-is-agent-adapters'
$runtimeRoot = 'D:\src\world-is-agent'
$protocolRepository = $runtimeRoot
```

Each game pins its tested Protocol release in its own `protocol.version`. Stardew Adapter `0.1.1` and RimWorld Adapter `0.1.0` pin `protocol-v1alpha2.0`, which resolves locally to commit `950d417fcc64517d8d59c334a4cf96ec224bd61a`. Build, test, install, and release commands use explicit `ProtocolRepository` and `ProtocolDir` inputs. The source checkout can be obtained from `https://github.com/zlci7/world-is-agent.git`. Export the required tag's Protocol files; repeat this for each game if their pins differ. Tag publication belongs to the repository owner, and the required tag must be available before building.

```powershell
$pin = (Get-Content -Raw -LiteralPath "$adapterRoot\stardew-valley\protocol.version").Trim()
git -C $protocolRepository rev-parse --verify "refs/tags/$pin^{commit}"
$exportRoot = Join-Path $env:TEMP ('wia-protocol-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $exportRoot | Out-Null
$archive = Join-Path $exportRoot 'protocol.zip'
git -C $protocolRepository archive --format=zip --output=$archive "refs/tags/$pin" protocol
Expand-Archive -LiteralPath $archive -DestinationPath $exportRoot
$protocolDir = Join-Path $exportRoot 'protocol'
```

Use `$adapterRoot\stardew-valley\README.md` for Stardew Valley commands and `$adapterRoot\rimworld\README.md` for RimWorld commands. Each directory provides self-contained build, test, package, and install scripts. The adapter repository also provides the optional repository guard `scripts/check-architecture.ps1`.

```powershell
$gamePath = 'D:\SteamLibrary\steamapps\common\Stardew Valley'
& "$adapterRoot\stardew-valley\scripts\build-adapter.ps1" -GamePath $gamePath -ProtocolRepository $protocolRepository -ProtocolDir $protocolDir
& "$adapterRoot\stardew-valley\scripts\test-adapter.ps1" -GamePath $gamePath -ProtocolRepository $protocolRepository -ProtocolDir $protocolDir
& "$adapterRoot\stardew-valley\scripts\install-stardew-adapter.ps1" -GamePath $gamePath -ProtocolRepository $protocolRepository -ProtocolDir $protocolDir

& "$adapterRoot\rimworld\scripts\build-adapter.ps1" -ProtocolRepository $protocolRepository -ProtocolDir $protocolDir
& "$adapterRoot\rimworld\scripts\test-adapter.ps1" -ProtocolRepository $protocolRepository -ProtocolDir $protocolDir
& "$adapterRoot\rimworld\scripts\install-rimworld-adapter.ps1" -GamePath 'D:\Games\RimWorld' -ProtocolRepository $protocolRepository -ProtocolDir $protocolDir
```

Release scripts validate game-specific `-ReleaseTag` values. Stardew packaging requires the full adapter repository commit through `-SourceCommit`; RimWorld can read it from the current checkout. Exported source snapshots supply their original commit explicitly. Adapter source is available on GitHub. Packages remain local acceptance artifacts; no Adapter release has been published.

## Documentation Expectations

Update docs in the same change when behavior changes:

- User-facing capability or limit: update [../STATUS.md](../STATUS.md).
- Public direction or positioning: update [../../README.md](../../README.md) and [../STATUS.md](../STATUS.md).
- Architecture boundary or lifecycle concept: update [../../ARCHITECTURE.md](../../ARCHITECTURE.md) or the relevant ADR.
- Setup or validation command: update [testing.md](testing.md).
- Adapter-specific behavior: update the corresponding game README in the local `world-is-agent-adapters` repository.

## Naming

Use `World Is Agent` and `WIA` in public-facing text. Identifier namespaces (`gameagent`, `GameAgent.Protocol`, `GameAgent.Stardew`, `GameAgentStardew`) are published compatibility and build contracts: keep them as they are, and do not rename them as part of a documentation or branding change. Historical phase records under `docs/` keep their original wording.

## Protocol Changes

Protocol changes should be explicit and additive whenever possible.

When editing `protocol/proto/gameagent.proto`:

- Regenerate Go and C# bindings.
- Run protocol static and generation checks.
- Update docs that describe message semantics.
- Keep Runtime and Adapter changes in the same change when both sides must move together.

## Adapter Changes

Adapters may use game-specific SDKs and concepts. Keep those details inside the adapter boundary.

For Stardew changes:

- Respect SMAPI threading rules.
- Keep Stardew live objects out of pure mapper/factory tests.
- Update adapter tests when protocol mapping, capability behavior, or dialogue UX changes.
- Keep manual smoke test notes current.

## Checklist

- Scope is clear and focused.
- Runtime remains game-agnostic.
- Protocol, Runtime, and Adapter changes are aligned when contracts move.
- Public docs reflect changed capabilities and limits.
- Relevant Go, protocol, C#, or manual smoke checks were run.
- Secrets and API keys are not committed.
