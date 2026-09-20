# Architecture

World Is Agent (WIA) is an open runtime for building AI agents inside virtual worlds.

The architecture is organized around one idea: worlds keep their native rules and execution, while WIA provides the runtime layer that gives entities identity, memory, context, and agentic behavior.

## The Shape Of WIA

```text
          Virtual World

     Minecraft / Unity / Games
              |
              |
        WIA Adapter
              |
              |
        WIA Runtime
 ┌────────────┼────────────┐
 Identity   Memory     Context
              |
           Agent
              |
            LLM
```

This is the core mental model:

- Virtual worlds own native state, rules, physics, UI, and execution.
- Adapters translate world-specific APIs into WIA protocol messages.
- The Runtime coordinates identity, memory, context, tools, turns, traces, and lifecycle.
- Agents make decisions through model calls and available capabilities.
- LLM providers are replaceable behind a provider-neutral interface.

## Layer Responsibilities

```text
Agent owns intent.
Runtime owns cognition.
Protocol owns contracts.
Adapter owns translation.
Game owns execution.
```

### Virtual World

The world is the source of truth for current state and real effects.

Examples include game engines, mod APIs, simulations, and custom virtual environments. Stardew Valley and RimWorld are the current real validation worlds.

### WIA Adapter

An adapter connects a specific world to WIA.

It provides four protocol surfaces:

```text
Event        what happened
Observation  what is true now
Capability   what this entity can do
Action       execute this capability in the world
```

Adapter code owns game-specific concepts such as NPC objects, map tiles, UI menus, pathfinding, schedules, weather, and mod API threading rules.

### WIA Runtime

The Runtime is reusable infrastructure for agent execution.

It owns:

```text
AgentSession identity
AgentTurn lifecycle
bounded multi-step execution
tool scheduling
bounded recent memory, session history, and summaries
context projection
trace and observability
timeouts and cancellation
async action suspend / resume
```

The Runtime communicates through protocol contracts and provider-neutral model types.

The Runtime also owns Game Profiles: agent configuration, prompts, policies, and definition catalogs. A process loads one selected profile as one published runtime instance. HTTP status and gateway admission read the same instance snapshot. Matching adapters may connect through multiple EnvironmentSessions; another game's adapter is rejected before environment readiness.

### Identity, Memory, Context

These are the core cognitive layers of WIA.

- Identity decides which agent a world event belongs to.
- Memory stores agent state under that identity. The default MVP0 backend is SQLite, physically separated by game and world and scoped by entity, with an in-memory backend available for tests and local runs.
- Context builds the model input from current observation, recent memory, transcript, tools, and runtime policy.

### Agent

An agent runs inside an AgentTurn.

The current runtime models agents as `AgentSession` and `AgentTurn` concepts. `AgentSession` is the logical identity and state scope for an entity. `AgentTurn` is one bounded execution triggered by a world event.

A turn observes the target entity, builds context, calls the model, executes tool calls, receives results, and either continues or settles.

### LLM

LLM providers are implementation details behind the runtime model interface.

The current codebase includes Fake, DeepSeek, and OpenAI providers.

## Runtime Loop

At a high level, one WIA turn looks like this:

```text
World Event
  -> AgentSession resolution
  -> Observe target entity
  -> Build context
  -> Model decision
  -> Tool call
  -> Action request
  -> Action result
  -> Memory / trace update
  -> Turn completion
```

Multi-step turns repeat the model decision and tool result feedback loop within configured budgets.

Async actions can suspend a turn, wait for a terminal result, re-observe the world, and resume the same turn.

## Adapter Boundary

Adapters are allowed to know the game deeply.

The Runtime expects adapters to translate that knowledge into stable protocol messages:

```text
Game-specific API
  -> Adapter-owned mapping
  -> WIA Protocol
  -> Runtime-owned execution
```

This lets new world integrations focus on adapter development while sharing the same runtime.

## Current Implementation

The current MVP0 implementation includes:

- Go Runtime over gRPC bidirectional streaming.
- Protocol v1alpha2.
- Stardew Valley SMAPI adapter.
- RimWorld 1.6 adapter.
- Embedded `stardew-valley` and `rimworld` Game Profiles selected through the local console.
- Stable agent identity by game, world, and entity.
- SQLite-backed memory, physically separated by game and world and scoped by entity, with bounded recent memory, session history, summaries, and history retrieval; an in-memory backend remains for tests and local runs.
- Context projection with a deterministic estimated-token budget.
- Bounded multi-step AgentTurn.
- Dynamic capability-driven tools.
- Sync and async action lifecycle.
- JSONL turn trace.
- Stardew dialogue, player input, emote, face-player, and same-location movement validation.

Current architecture limits:

- Async action waiting is process-local.
- Same-agent FIFO scheduling is validated within one live EnvironmentSession.
- Cross-stream recovery and durable continuation are future work.
- Memory is independent of the game save: records from an abandoned branch can become visible again when comparable game time catches up.
- One Runtime process loads one Game Profile; a saved profile change takes effect after restart.
- Multiple same-game streams are separate EnvironmentSessions and do not provide a concurrent same-save write guarantee.

## Further Reading

- [Current status](docs/STATUS.md)
- [Development guide](docs/development/guide.md)
- [Runtime Architecture Baseline](<docs/summary/GameAgent Runtime 整体架构设计规范.md>)
- [Multi-game Compatibility and Agent Binding](<docs/summary/GameAgent 多游戏兼容性与 Agent Binding 决策.md>)
- [Official Adapter workflow](docs/development/guide.md#official-adapters)
