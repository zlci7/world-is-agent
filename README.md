# World Is Agent

**World Is Agent (WIA)** is an open, game-native agent runtime. Games report events and observations through adapters; the Runtime gives characters identity, context, memory, model-driven decisions, and bounded tool execution.

![World Is Agent](docs/images/world-is-agent.jpg)

## What Is Included

- A Go Runtime with gRPC bidirectional streaming and a local browser console.
- Protocol v1alpha2 for events, observations, capabilities, actions, and turn completion.
- Persistent, game/world/entity-scoped memory and JSONL turn traces.
- Provider-neutral model support with DeepSeek and OpenAI implementations.
- Experimental Stardew Valley and RimWorld adapters.
- Embedded Runtime-owned Game Profiles for `stardew-valley` and `rimworld`.

One Runtime process loads one Game Profile. Multiple adapters for the loaded game may connect as separate environment sessions; an adapter for another game is rejected before environment readiness and capability discovery.

## Quick Start

Build the portable Windows Runtime package:

```powershell
.\scripts\release-runtime.ps1
```

Extract `dist\world-is-agent-v<version>-windows-amd64.zip`, run `wia-runtime.exe`, and use the browser console:

1. Choose Stardew Valley or RimWorld. The Runtime prepares missing shipped profile files in its data root.
2. Configure a model provider and API key if required.
3. Wait for `Ready`, then start the matching game and adapter.

Choosing another game while Ready saves it as **Next Game**. The current profile and active connections remain in use until the Runtime restarts.

The Runtime package does not include game adapters. Build and install them separately using the [Stardew adapter guide](adapters/stardew/README.md) or [RimWorld adapter guide](adapters/rimworld/README.md).

For development, run `.\scripts\start-runtime.ps1`. It uses `WIA_DATA_ROOT` when set, otherwise `runtime/.local/runtime-data`, and does not select a game implicitly. See the [development guide](docs/development/guide.md).

## Technology

`Golang` · `gRPC` · `Protobuf` · `SQLite` · `C#` · `SMAPI` · `LLM Tool Calling` · `JSON Schema`

## Repository Layout

```text
runtime/      Runtime, embedded Game Profiles, prompts, and definitions
protocol/     Protobuf contracts and generated bindings
adapters/     Stardew Valley and RimWorld adapters
console/      Local browser client
docs/         Architecture, status, development, and historical records
```

The adapters remain in this repository. Their planned move to `world-is-agent-adapters` is a later productization step.

## Documentation

- [Architecture](ARCHITECTURE.md)
- [Current status](docs/STATUS.md)
- [Development guide](docs/development/guide.md)
- [Testing and acceptance](docs/development/testing.md)
- [Documentation index](docs/README.md)

## Status

Game Profile selection and multi-game Runtime bootstrap are implemented. Automated validation covers the affected bootstrap, HTTP, configuration, definition, command, and gateway paths; full release and browser validation are being finalized. User acceptance with both real games is still pending. Earlier real-game observations are dated baselines and do not constitute Game Profile switching acceptance.

## License

[MIT License](LICENSE)
