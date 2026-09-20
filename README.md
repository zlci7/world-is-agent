# World Is Agent

**World Is Agent (WIA)** is an open, game-native agent runtime. Games report events and observations through adapters; the Runtime gives characters identity, context, memory, model-driven decisions, and bounded tool execution.

![World Is Agent](docs/images/world-is-agent.jpg)

## What Is Included

- A Go Runtime with gRPC bidirectional streaming and a local browser console.
- Protocol v1alpha2 for events, observations, capabilities, actions, and turn completion.
- Persistent, game/world/entity-scoped memory and JSONL turn traces.
- Provider-neutral model support with DeepSeek and OpenAI implementations.
- Game Profiles for the independently distributed official Stardew Valley and RimWorld adapters.
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

Choose **Switch game** to apply another profile in the running Runtime. Active turns are canceled, recorded history is retained, and the matching adapter reconnects. **Model settings** lets you change the provider, model, API key, and optional base URL; the candidate is tested before it is applied.

Automatic reconnection requires Stardew Adapter 0.1.1 or the current RimWorld Adapter 0.1.0. The Runtime package does not include game adapters. Build and install them from [world-is-agent-adapter](https://github.com/zlci7/world-is-agent-adapter). See [Official Adapters](docs/development/guide.md#official-adapters).

For development, run `.\scripts\start-runtime.ps1`. It uses `WIA_DATA_ROOT` when set, otherwise `runtime/.local/runtime-data`, and does not select a game implicitly. See the [development guide](docs/development/guide.md).

## Technology

`Golang` · `gRPC` · `Protobuf` · `SQLite` · `C#` · `SMAPI` · `LLM Tool Calling` · `JSON Schema`

## Repository Layout

```text
runtime/      Runtime, embedded Game Profiles, prompts, and definitions
protocol/     Protobuf contracts and generated bindings
console/      Local browser client
docs/         Architecture, status, development, and historical records
```

Official adapters live in the independent [world-is-agent-adapter](https://github.com/zlci7/world-is-agent-adapter) Git repository. This repository contains the Runtime, Console, Protocol, and Runtime-owned Game Profiles.

## Documentation

- [Architecture](ARCHITECTURE.md)
- [Current status](docs/STATUS.md)
- [Development guide](docs/development/guide.md)
- [Testing and acceptance](docs/development/testing.md)
- [Documentation index](docs/README.md)

## Status

Game Profile selection, multi-game Runtime bootstrap, and the official Adapter repository split are implemented. The user reported the Phase 10.4/10.5 real-game baseline passed. Live game switching and model settings have separate focused acceptance steps in the [testing guide](docs/development/testing.md).

## License

[MIT License](LICENSE)
