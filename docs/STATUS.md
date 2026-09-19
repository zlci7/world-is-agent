# Status

World Is Agent is in experimental MVP0 development.

MVP0 is now frozen for release: the current version is stabilized and documented for external use, and the project is not pursuing a fully autonomous agent (self-directed goals, long-horizon autonomous planning, or multi-agent orchestration) in this version. See `AGENTS.md` for the current working boundaries.

This document is the public source of truth for current repository capabilities, validation scope, and known limits.

Last updated: 2026-09-19.

## Validation Scope

The current validated path is:

```text
Stardew Valley + SMAPI
  -> WIA Stardew Adapter
  -> WIA Protocol v1alpha2
  -> Go Runtime
  -> LLM Provider
```

Stardew Valley is the first real adapter and validation environment.

## Implemented

| Area | Current support |
| --- | --- |
| Runtime transport | gRPC bidirectional streaming between adapter and runtime. |
| Environment bootstrap | Adapter hello, environment ready, capability publication. |
| Protocol | `gameagent.protocol.v1alpha2` with world scope, target entity routing, action lifecycle, and turn completion messages. |
| Identity | `AgentSessionKey = game_id + world_id + entity_id`. |
| Scheduling | Per-EnvironmentSession same-agent FIFO lane scheduling. |
| AgentTurn | Bounded multi-step turns with model/tool feedback. |
| Tools | EnvironmentSession-scoped dynamic capabilities with immutable Turn Tool View snapshots used by model exposure and Scheduler lookup. |
| Tool policy | Capability metadata for exclusive-per-step and settle-after-success behavior. |
| Actions | Sync action result handling and async action status/result handling. |
| Turn completion | Runtime can send best-effort `TurnCompletion` to the adapter. |
| Memory | SQLite-backed Recent Memory, physically separated by game/world and scoped by entity, with metadata validation, idempotent transactions, and game-time filtering. Defaults: 100 retained records and 400 projection credentials per entity; context takes at most 5 records within 4096 estimated tokens. |
| Definitions | Optional static Runtime catalog for Game Definitions and Agent Definitions scoped by `game_id + definition_id`. |
| Context | Runtime Context Engine builds stable Context Projection from event, ContextFacts, observation, recent memory, current-turn transcript, definitions, Agent Instance Descriptor, runtime policy, and Turn Tool View snapshot. |
| Context budget | Deterministic provider-neutral estimated-token budget selection, section cropping, tool size admission, and final request hard gates. |
| Context diagnostics | `ContextBuildReport` and bounded `ToolAdmissionReport` summaries record fallback, cropping, dropped content, tool admission, and final request estimated-token size. |
| Providers | Provider-neutral model interface with Fake, DeepSeek, and OpenAI implementations. |
| Trace | JSONL turn trace written under the Runtime data root at `data/traces.jsonl`, including bounded context request summaries. |
| Data root | Every Runtime-owned path resolves from one data root instead of the process working directory. Precedence: `--data-root`, then `WIA_DATA_ROOT`, then the platform data directory (`%LOCALAPPDATA%\WorldIsAgent`, `~/Library/Application Support/WorldIsAgent`, or `$XDG_DATA_HOME/wia`). Absolute configured paths are used as-is; relative ones resolve against the data root. |
| Startup | Bootstrap and the agent core are separate: the Runtime serves gRPC even when no model configuration exists, reporting `needs_configuration` with the expected path instead of exiting. A data root that could not be given its shipped configuration reports `blocked` instead, because configuring a model cannot resolve it. |
| First-run configuration | An empty data root is given the shipped agent profile and definitions, and the model configuration is written from the local client: the Runtime probes the exact provider, model and key it is about to write, and only a passing probe writes the credential file, `config/model.json`, and installs the agent core. A failed probe writes nothing. Stored credentials support `env:VARIABLE` and `file:relative-path` references; on Unix the secrets directory is `0700` and the credential file `0600`. || Local client | A local HTTP control plane serves an embedded client showing Runtime state, the data root, a credential-free model summary, and recent AgentTurns projected from the trace; on a data root with no model configuration it serves the first-run setup card instead. The Runtime opens the browser itself and hands over a session credential in the URL fragment, so nothing is copied by hand. The listener is loopback only, the Host header must be a loopback name, every `/api` route but the session exchange requires the session cookie, and no route returns a credential. The client is built from `console/web` and embedded into the binary; it is not distributed as a prebuilt artifact yet. |

Memory validation: Phase8.1 is accepted. Automated Store/Loop reconstruction and real dialogue persistence/readback are covered. Real Runtime process-restart recovery, the specified cross-version end-to-end path, and race validation remain open; see the [acceptance record](phase08/GameAgent MVP0 Phase8.1 技术开发与验收方案.md#验收结论). SQLite is independent of the game save: future comparable GameTime is filtered, but abandoned-branch history may become visible when game time catches up.

## Experimental

| Area | Current support |
| --- | --- |
| Stardew adapter | Real SMAPI adapter used as the first validation adapter. |
| Stardew observation | Adapter-owned Stardew fact projection for time, weather, scene, relationship, schedule, conversation, and nearby NPC context. |
| Stardew interaction events | NPC interaction and player dialogue input events. |
| Stardew dialogue | Native NPC dialogue line followed by generated reply choices and optional free-text input. |
| Stardew actions | `speak`, `emote`, `present_dialogue`, `face_player`, and same-location `move_to`. |
| Stardew mail capability | `send_mail` wraps MailFrameworkMod as an optional dependency: published only while that mod is installed, absent with an explicit `mail_framework_unavailable` code otherwise, and never a hard dependency. Text is validated against a closed character set before it reaches the game, because letter text is parsed as game tokens. Verified on a real machine: publication, the missing-mod path, delivery and reading of a letter, and one autonomous selection — the system prompt does not name the capability, and the model chose it from its tool view to answer a player request for deferred delivery. Not covered: a persona sample set across NPCs and scenarios (one cross-NPC sample was taken), and a dedicated negative scenario set. |
| Model providers | DeepSeek and OpenAI providers work through local config and external API keys. |
| Architecture checks | Local scripts exist for protocol and architecture checks. CI enforcement is still evolving. |

## Not Yet Supported

| Area | Current limit |
| --- | --- |
| Full terminal history and compaction | Phase8.2 is a design draft for complete agreed terminal-turn sources, synchronous summaries, and a bounded recent-history tail; current Recent records are not a full conversation archive. |
| History retrieval and retention | Phase8.3 is a design draft for literal source retrieval and optional cleanup, disabled by default. |
| Long-term semantic memory | No Semantic extraction/correction pipeline, vector store, or embedding index. |
| Provider-specific token sizing | Context budget uses deterministic estimated tokens, not exact provider tokenizer or automatic model window detection. |
| Automatic reconnect | Adapter reconnect and environment recovery are future work. |
| Durable async continuation | Async action waiting is process-local. |
| Cross-stream ordering | Same-agent FIFO is validated inside one live EnvironmentSession. |
| Multiple heterogeneous worlds | The runtime architecture allows adapters, but only Stardew has real validation. |
| Heartbeat/liveness productization | Heartbeat exists in protocol but is not yet a completed recovery mechanism. |
| Scenario evaluation | No public scenario evaluation suite yet. |
| Packaged release | `scripts/release-runtime.ps1` builds a portable package: `world-is-agent-v<version>-windows-amd64.zip` containing `wia-runtime.exe`, a README and the license. The executable embeds the local client and the shipped agent configuration, so it runs with no arguments from any directory and needs no installer, no administrator rights, and neither Node.js nor Go on the path. The version comes from the repository `VERSION` file and is written into the binary at link time. The package carries no game adapter: adapters compile against a local game installation and are installed separately as mods. |

First-run configuration validation: the empty data root path is covered end to end. At the HTTP surface, a fresh root reports `needs_configuration`, the shipped profile and 35 definitions are seeded, the setup submission writes a credential reference and a window, the Runtime reaches `ready`, and a restart keeps both; a real process started from outside the repository was exercised against a live provider, and the missing-key, unreachable-provider, unknown-provider and rejected-key paths each reported their own code and wrote nothing. In a browser against an empty data root, the setup card renders, the provider list and model default come from the Runtime and follow the selection, Save stays disabled without a key, and a rejected key shows the probe's message while leaving both the form and the data root untouched.

A full first run was then walked on a real machine with a real key: the card, the save, `Runtime Ready ✓`, a refresh, a restart with no second seed, and a game session in which talking to an NPC produced complete turns whose traces show the whole pipeline — observation, history, model request and response, tool selection, action result, and a written memory record. Known limits: the console shows a turn summary and has no drill-down, so per-turn prompt and tool detail is only in `data/traces.jsonl`; Windows inherits the secrets directory permissions rather than tightening them; and only one Runtime process may use a data root at a time.

Portable release validation: the 0.1.0 archive was extracted outside the repository and run against an empty data root. It seeded the shipped configuration and 35 definitions, served the embedded client, reported `version=0.1.0` and `state=needs_configuration` on `/api/status`, and reached `ready` unchanged against an already-configured root. Not covered: a signed build, a macOS or Linux package, and an installer, none of which this version produces.

## Maintenance Rule

When a PR changes a user-visible capability, validation scope, or known limit, update this file in the same PR.
