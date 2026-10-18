# Hosted invitation beta acceptance

Date: 2026-10-18

Status: development source and local background validation complete; deployment-host and real-player acceptance pending. This is not a public service availability or production-readiness claim.

## Delivered scope

| Area | Source behavior |
| --- | --- |
| Accounts | Independent invitation + email/password registration, stable server-generated `user_id`, versioned Argon2id hashes, web sessions, CSRF, private recovery/disable commands and audit. |
| Device access | Personal expiring WIA Keys with one-time plaintext, installation binding, revocation and separate internal worker credentials. |
| Ownership | One private Runtime/container/volume per account; single controller allocation owner, generation checks, deployment-scoped shutdown, missing/foreign-volume recovery blocking. |
| Gateway | Certificate-verified public TLS client path, Hello/version checks, fixed account routing, same-device replacement and another-device rejection. |
| Model | Private fixed-DeepSeek proxy, explicit input/output/time/concurrency budgets, atomic UTC-day reservations, usage settlement, persistent unknown outcomes and active cancellation. |
| Website | Chinese pixel registration/login, connection instructions, key management, read-only tasks, recent turns, preferences and usage; explicit failure/retry and account clearing. |
| Preferences | Bounded nickname/language/response length, versioned updates and separately projected Runtime context; temporary read failures retain the last confirmed values. |
| Stardew Adapter | 0.1.3 source with Local/Hosted mode, HTTPS, WIA Key/device metadata, F8 settings, capped backoff, Chinese errors and optional packaged service address. |
| Operations | Linux amd64 entry points, Dockerfile, Compose, Caddy, example configuration and deployment-scoped closed-volume backup procedure. |

The existing Protocol remains unchanged. Local Runtime configuration remains available. The implementation plan is [Phase11.2](phase11.2-hosted-runtime-and-launch.md); operator commands and player setup are in the [deployment guide](../../deploy/hosted/README.md).

## Completed local validation

- Full Go repository regression passed with `go test -p 1 ./... -count=1 -timeout=8m`. Subsequent hosted changes passed the focused hosted suite under the race detector.
- Windows race checks passed for `accounts`, `hosted` and `workerapi` using the existing portable GCC toolchain. No privileges or ACLs were changed.
- The account suite covers one-use invitations, persistent identity/session/key ownership, replacement keys, preference conflicts, password recovery, concurrent quota admission, retained unknown reservations, request deduplication and SQLite snapshot restore.
- Hosted integration tests use a real local TLS gRPC gateway, verified test CA and independently bootstrapped Runtime instances. Identical game/world/NPC/experience identifiers retain separate facts after worker reconstruction. Private query authentication and public management-path rejection pass.
- Fake Docker-engine tests cover concurrent allocation, controller/worker restart, retained volumes, generation rotation, old credential rejection, missing/inventory/ownership failures and deployment-scoped shutdown. They do not run Docker containers.
- A blocking stub upstream confirms that key disconnection cancels an active model request; its uncertain reservation stays charged. Recovery blocking, transient service failures and capacity have distinct client outcomes.
- Linux amd64 hosted and worker binaries cross-compile with CGO disabled. The backup script passes Bash syntax validation.
- Frontend type checking, latest-response ordering and production builds pass. Background Chromium checks use the real account HTTP surface, built assets and isolated fake-provider workers. Registration, key creation/hiding, preference save, logout clearing and the offline Task view pass at desktop/mobile widths; 320px has no horizontal overflow.
- Injected HTTP read failures cover keys, connection information, preferences, quota and turns. Initial failure is explicit; later failure marks retained values and last-success time. Refresh recovers the affected reads.
- Independent interface finish review cleared its three material findings; the design documenter confirmed the incumbent system without changing it. Backend review was performed by the implementing agent, not an independent security audit.
- Six Stardew test projects passed the full background baseline of 544 tests. The final RuntimeClient change then passed its expanded 48-test suite and a Release build of the Mod. This does not prove native game input or hosted connectivity.
- Main and Adapter architecture checks, Protocol static checks and Go generation checks pass. Documentation links and `git diff --check` are checked at delivery.

## Required deployment and game evidence

| Check | Required result |
| --- | --- |
| Linux Docker startup | Build both images on the deployment host; confirm non-root worker data initialization, private service discovery, one writer per volume, graceful drain and same-volume restart. |
| Public ingress | Resolve real web/gateway DNS, issue and renew certificates, and exercise the actual bidirectional TLS gRPC stream through Caddy. |
| Player installation | A clean Windows player machine with SMAPI + matching Mod + WIA Key completes dialogue without a local Runtime or LLM credential. Verify the native F8 form and input. |
| Recovery | Real game save/load, appointment success/expiry, disconnect, worker/controller restart, revocation/expiry and uncertain effects retain factual outcomes. |
| Full backup/restore | Restore the complete stopped deployment on an isolated host with original secrets, IDs and volume labels; verify memory, tasks, quotas and a missing-checkpoint block. SQLite account-only restore is insufficient evidence for this check. |
| Capacity and operation | Measure host/model capacity and password-hash cost; set invitation count, budgets, retention, alerts, backup frequency and recovery target; rehearse update and rollback. |

No Docker engine or target public deployment was available for this validation. No game was activated and the built Mod was not installed into the player's game.

## Current limits

The beta does not verify mailbox ownership, send email, support public self-registration or payments, enforce per-account disk quotas, automatically evict idle workers, or supply an integrated monitoring platform. The example capacity is not a measured guarantee. Ordinary in-flight Action continuation remains process-local, and ordinary Memory retains its existing save-branch isolation limit. An offline local game does not advance through server-side simulation.
