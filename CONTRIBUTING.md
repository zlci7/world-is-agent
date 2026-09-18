# Contributing

World Is Agent is experimental. Contributions should keep the Runtime / Protocol / Adapter boundary clear and keep public documentation aligned with implementation.

## Development Principles

- Runtime code stays game-agnostic.
- Protocol changes describe cross-boundary contracts.
- Adapter code owns game-specific APIs, UI, threading, and execution rules.
- Provider code stays behind the runtime model interface.
- Documentation changes should state current behavior clearly.

## Repository Areas

```text
runtime/     Go runtime, gateway, loop, scheduling, memory, trace, providers
protocol/    Protobuf contract and generated bindings
adapters/    Game-specific adapters
docs/        Status, guides, ADRs, phase plans, acceptance records
scripts/     Local validation and helper scripts
```

## Documentation Expectations

Update docs in the same PR when behavior changes:

- User-facing capability or limit: update [docs/STATUS.md](docs/STATUS.md).
- Public direction or positioning: update [README.md](README.md) and [docs/STATUS.md](docs/STATUS.md).
- Architecture boundary or lifecycle concept: update [ARCHITECTURE.md](ARCHITECTURE.md) or the relevant ADR.
- Setup or validation command: update [docs/development/testing.md](docs/development/testing.md).
- Stardew-specific behavior: update [adapters/stardew/README.md](adapters/stardew/README.md).

## Naming

Use `World Is Agent` and `WIA` in public-facing text. Identifier namespaces (`gameagent`, `GameAgent.Protocol`, `GameAgent.Stardew`, `GameAgentStardew`) are published compatibility and build contracts: keep them as they are, and do not rename them as part of a documentation or branding change. Historical phase records under `docs/` keep their original wording.

## Protocol Changes

Protocol changes should be explicit and additive whenever possible.

When editing `protocol/proto/gameagent.proto`:

- Regenerate Go and C# bindings.
- Run protocol static and generation checks.
- Update docs that describe message semantics.
- Keep Runtime and Adapter changes in the same feature PR when both sides must move together.

## Adapter Changes

Adapters may use game-specific SDKs and concepts. Keep those details inside the adapter boundary.

For Stardew changes:

- Respect SMAPI threading rules.
- Keep Stardew live objects out of pure mapper/factory tests.
- Update adapter tests when protocol mapping, capability behavior, or dialogue UX changes.
- Keep manual smoke test notes current.

## Testing

Use [docs/development/testing.md](docs/development/testing.md) for the current check list.

At minimum, a PR should run the tests that cover the modified area and document any check that could not be run locally.

## Pull Request Checklist

- Scope is clear and focused.
- Runtime remains game-agnostic.
- Protocol, Runtime, and Adapter changes are aligned when contracts move.
- Public docs reflect changed capabilities and limits.
- Relevant Go, protocol, C#, or manual smoke checks were run.
- Secrets and API keys are not committed.
