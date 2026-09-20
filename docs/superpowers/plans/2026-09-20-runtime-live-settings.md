# Runtime Live Settings Implementation Plan

> For agentic workers: use superpowers:subagent-driven-development. Apply the task ownership and verification contract below.

Goal: change game and LLM from the console without restarting the Runtime.
Architecture: retain the process listeners and coordinator; replace a fully owned runtime bundle after canceling and draining the previous generation. Model probe precedes reconfiguration, with coherent disk commit and rollback. Stardew retries disconnected streams.
Tech Stack: Go, gRPC, Vue/TypeScript, C# net6/SMAPI.
Spec: docs/superpowers/specs/2026-09-20-runtime-live-settings.md

## Global Constraints

- Switching immediately cancels active turns and preserves recorded history.
- Protocol schema, Mod identities, transports and game business capabilities remain unchanged.
- Main and Adapter repositories remain separate and development stays on main.
- No real model credentials or game saves are used for automated tests.
- Failed candidate validation must not disrupt an existing healthy bundle.
- Model credentials and config references must commit coherently.

## Task 1: Runtime ownership and live configuration

Files: runtime/internal/bootstrap, gateway, httpapi, llm and directly needed lifecycle tests.
Consumes: existing SelectGame, ApplyModelConfiguration, Connect and Close contracts.
Produces: same POST /api/setup/game and /api/setup/model routes applying immediately, state reconfiguring while draining, restart_required=false; ConfigSummary retains its existing credential-free fields; persisted base URLs are not returned.

- [x] Add failing tests for immediate Ready game replacement, model replacement, active/idle/partial-handshake stream retirement, cancellation and preservation of terminal history, old-generation isolation, concurrent setup/close, validation/prepare/commit rollback and credential consistency.
- [x] Replace the frozen-bundle control flow with a serialized reconfiguration transaction. Bind gateways to their own loop. Register and cancel all streams through the runtime generation before handshake; prevent shutdown/late admission races. Drain before closing stores and reopening conflicting databases.
- [x] Allow Ready model edits. Probe the exact request through the existing HTTP route. Stage private credentials and atomic config references, rollback failed apply, and preserve current instance on preflight failures.
- [x] Update prior frozen-game/one-time-model tests to assert the new coherent immediate-apply contract. Run focused Go tests and race checks for amended concurrency, recording failures and successful output.
- [x] Prepare a task report; root reviews and commits this component separately.

## Task 2: Console settings

Files: console/web/src/App.vue, GameSetup.vue, Setup.vue, types.ts, api.ts and focused interaction checks.
Consumes: Task 1 routes and immediate-apply response; existing ConfigSummary fields.
Produces: Switch game, persistent Model settings button, editable provider/model/base URL/key and cancel form; shared mutation busy state and latest-response protection.

- [x] Change game labels/help to immediate application and remove restart/next-game instructions.
- [x] Expose model form when Ready on explicit button activation. Prefill provider/model, require a supplied API key and an explicit custom base URL when needed, clear it after success/close, and keep a failed form visible. Preserve first-run model setup.
- [x] Disable conflicting configuration submissions during game switching/model probing. Show switching/applying progress and success through current status, with errors that preserve usability.
- [x] Run type-check and existing response-order tests; root verifies real browser flows with local stub after backend integration.
- [x] Write report for task review and local commit.

## Task 3: Stardew automatic reconnect

Files: world-is-agent-adapters/stardew-valley/src/Runtime/RuntimeClient.cs, focused lifecycle helper/tests, README and VERSION.
Consumes: disconnect/EOF or transient rejection from unchanged Protocol; existing main-thread dispatcher and connection epoch.
Produces: bounded automatic retry, fresh session bootstrap/world binding, stale callback cancellation on manual reconnect/Dispose.

- [x] Add focused failing lifecycle tests for EOF/error retry, retry cancellation, disposal, manual reconnect overlap, repeated mismatch and old callbacks.
- [x] Implement a single retry owner with main-thread reset and epoch validation. Avoid unbounded tasks, concurrent streams and replay of obsolete actions.
- [x] Set Stardew VERSION to 0.1.1 and document automatic recovery; retain pin and Mod identity.
- [x] Run affected tests, full Adapter tests and release against the pinned Protocol export; temporary targets only.
- [x] Write report for review and local commit.

## Task 4: Integration and delivery

- [x] Review all component diffs, resolve findings, and save functional local commits.
- [x] Update current docs and runtime VERSION to 0.2.0 for a distinguishable feature acceptance package. Record user-reported prior baseline without claiming the new live workflow is real-game accepted.
- [x] Run full Go regression, frontend build/type/order, protocol and architecture checks. Build Runtime and affected Adapter packages.
- [x] Exercise a real Runtime process and browser with a local LLM stub: same-process game switching, model edits, failed probe retains prior model, browser session survives; verify packet-level adapter re-admission and mismatch separately in integration tests.
- [x] Final whole-change review and delivery of clean local main commits and packages. No new release upload or push as part of this feature implementation.
