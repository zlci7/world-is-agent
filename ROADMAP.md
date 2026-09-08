# Roadmap

World Is Agent is an experimental MVP0 project. This public roadmap uses Now / Next / Later so the current direction stays readable while detailed phase plans continue to live under `docs/`.

## Now

Complete the Phase7 Context Subsystem for the Runtime + Stardew vertical slice.

Focus:

- Freeze the Context contracts before implementation expands.
- Add runtime-owned Game Definition and Agent Definition sources.
- Build scoped Context projection from event, observation, memory, transcript, definitions, and tools.
- Replace process-global environment tool exposure with EnvironmentSession-scoped Tool View snapshots.
- Make context selection, budget limits, and build diagnostics deterministic and testable.
- Validate the full path with real Stardew NPC dialogue.

Phase8.1 SQLite Recent Memory is implemented and accepted with documented limits. It preserves bounded per-agent records; full Runtime process-restart validation remains open. See the [acceptance record](docs/phase8/GameAgent%20MVP0%20Phase8.1%20技术开发与验收方案.md).

Exit signals:

- A model request clearly shows the right game definition, agent definition, event facts, observation, memory, transcript, and tool view.
- Two Stardew NPCs can load different definitions without adapter-side prompt assembly.
- Tool exposure and tool execution use the same immutable Turn Tool View.
- Phase5 multi-step and Phase6 async action behavior remain stable.
- Current capabilities and limits stay captured in `docs/STATUS.md`.

## Next

Develop Phase8.2 persistent terminal history and context compaction, then independently deliver Phase8.3 history retrieval and optional retention. Environment recovery uses the Phase8.1 persistent identity/read contract and does not require Phase8.2/8.3 completion.

Focus:

- Transactional migration from Recent records to complete agreed terminal-turn sources, with idempotent writes per AgentSession.
- A persisted summary plus recent original history, targeting about 20,000 recent tokens after compaction within the request budget.
- Synchronous, bounded compaction before a turn's first decision, with explicit failure and game-time visibility behavior.
- Bounded literal retrieval of retained history, including short Chinese queries, and source-aware context inclusion.
- Optional retention, disabled by default, limited to old summary-covered original history outside recent and in-use protection.
- Adapter reconnect and EnvironmentSession recovery.
- Heartbeat and liveness semantics.
- Capability registry scoping across reconnects.
- Disconnect, late result, and idempotency behavior.
- Durable continuation strategy for async actions.

Exit signals:

- Committed terminal history and summary checkpoints survive a real Runtime process restart and enter the final model request.
- Retained old details are retrievable; deleted original content is not presented as recoverable evidence.
- Runtime restart and adapter reconnect behavior are specified and tested.
- Async action outcomes have clear recovery semantics.

## Later

Grow WIA from one validated adapter into a reusable multi-world agent harness.

Focus:

- Scenario evaluation and regression suites.
- Fault injection for runtime, adapter, and provider boundaries.
- Adapter conformance tests.
- A second real adapter outside Stardew Valley.
- Versioned releases, migration policy, and trace retention/export.

Exit signals:

- New adapters can be built against documented conformance checks.
- Runtime behavior is measurable through scenario evaluation.
- Public releases have stable install, upgrade, and compatibility notes.

## Detailed Plans

Detailed phase plans, ADRs, and acceptance records are kept under [docs/](docs/README.md).

Memory phase contracts: [Phase8 overview](docs/phase8/GameAgent%20MVP0%20Phase8%20技术开发与验收方案.md), [Phase8.2](docs/phase8/GameAgent%20MVP0%20Phase8.2%20技术开发与验收方案.md), and [Phase8.3](docs/phase8/GameAgent%20MVP0%20Phase8.3%20技术开发与验收方案.md). Phase8.2/8.3 are implementation-plan drafts, not implemented capabilities.
