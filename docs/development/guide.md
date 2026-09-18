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
adapters/    Game-specific adapters
console/     Local client (Vue 3 + TypeScript + Vite) and the Go package that embeds its build
docs/        Status, guides, ADRs, phase plans, acceptance records
scripts/     Local validation and helper scripts
```

## Repository Model

WIA is organized as Runtime + Protocol + Adapter. Adapters are logically independent of this repository's directory layout: they depend on a versioned protocol instead of on `adapters/stardew` sitting at a known path. See [logical-separation.md](logical-separation.md) for the work that removes the remaining layout coupling.

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

## Adapter Capabilities From Third-Party Mods

Adapters may wrap another mod's API and expose it as a Capability, so the agent can select it like any other tool. Third-party mods stay optional dependencies: when one is absent, the adapter still loads and the capability is rejected with an explicit code.

The normative rules — optional dependency, public-interface access, main-thread execution, external text validation, direct execution without player confirmation, and the landing checklist — live in [mod-capability-integration.md](mod-capability-integration.md). The first integration built on them is [Phase10.1 邮件能力方案](../phase10/GameAgent%20MVP0%20Phase10.1%20Mod%20邮件能力技术开发方案.md).

## Documentation Expectations

Update docs in the same change when behavior changes:

- User-facing capability or limit: update [../STATUS.md](../STATUS.md).
- Public direction or positioning: update [../../README.md](../../README.md) and [../STATUS.md](../STATUS.md).
- Architecture boundary or lifecycle concept: update [../../ARCHITECTURE.md](../../ARCHITECTURE.md) or the relevant ADR.
- Setup or validation command: update [testing.md](testing.md).
- Stardew-specific behavior: update [../../adapters/stardew/README.md](../../adapters/stardew/README.md).

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
