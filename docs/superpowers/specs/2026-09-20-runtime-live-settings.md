# Runtime Live Settings

## User behavior

The console applies a selected game and model configuration in the running Runtime. HTTP and gRPC listeners retain their addresses and the browser session remains valid. One game is active at a time. Switching immediately cancels active turns and preserves recorded history. No manual Runtime restart is required.

A model settings button is available after initial setup. The form shows the configured provider and model without exposing a credential. A custom base URL is entered explicitly for each submission; an empty input selects the provider default. Persisted base URLs are not returned because they can contain credentials. Each submission supplies an API key and probes the exact candidate before applying it. Invalid credentials or an unsuccessful probe leave the active configuration unchanged. Provider choices remain the existing supported providers; no multi-model routing is added.

## Runtime ownership

The existing setup coordinator serializes game/model changes and shutdown. Validate candidate profile/catalog/provider inputs before interrupting the current instance. Publish a non-admitting reconfiguring state while draining the old bundle, then publish the complete new bundle and coherent status. Keep restart_required for API compatibility with a false value after successful application; configured_game and loaded_game agree when Ready.

A connection and its event handler belong to one bundle generation. An old stream must never invoke the replacement model, catalog or history. Close must terminate streams even if they are waiting for Hello or capabilities, cancel active/queued work, and wait for terminal recording and owned store release. A late old stream cannot start work against a closed store. Preserve the listeners and process-owned trace recorder.

Candidate preparation must respect task database exclusive ownership. Stop and release the old bundle before opening a conflicting candidate. If candidate initialization or config commit fails, restore a usable previous bundle and disk configuration. If cleanup/rollback cannot be made certain, report a clear blocked state rather than Ready. Failed candidate validation must not disrupt an existing healthy bundle.

Model credentials and config references must commit coherently. Use a freshly generated private credential file and atomically replace the model configuration that references it; a failed attempt must not overwrite an old key. Rollback must restore the prior selection/model document. Avoid copying secrets into status, errors, traces, docs or browser storage. Existing explicit config-path overrides retain their meaning.

## Adapter continuity

Stardew automatically retries a disconnected stream with a bounded delay, including Runtime reconfiguration and game mismatch. It repeats Hello, capability discovery and world binding; all game state access and reset remains on the main thread. Manual reconnect and Dispose invalidate stale retry callbacks. Reset stale dialogue/action state before a replacement session can send messages. RimWorld retains its existing retry mechanism.

## Scope and acceptance

Protocol schema, Mod identities, transports and game business capabilities remain unchanged. Main and Adapter repositories remain separate and development stays on main. The user reported the previous real-game baseline passed; new live settings require a fresh focused real-game check.

Automated evidence covers live bidirectional switching without process restart, active-turn cancellation with history completion, silent/late bootstrap stream closure, coherent concurrent commits and rollback, model changes and failed probes, credential confidentiality, and Adapter retry lifecycle. Build and browser-check the settings flows with a local model stub; no real model credentials or game saves are used for automated tests. Deliver local commits and uniquely versioned packages for CR; publication follows user approval.
