# Independent hosted administration acceptance

Date: 2026-10-19

Status: source and local background acceptance complete. Public deployment, actual mailbox delivery, complete Docker volume restoration, capacity and native game acceptance retain their separate release gates.

## Delivered scope

- Independent `/admin/login` and management routes, opaque light surfaces, Chinese task-focused navigation and a separate administrator session scope/cookie.
- Private administrator initialization, current-role checks and login audit; player sessions cannot authorize administration and management sessions cannot authorize player APIs.
- User search, invitation registration source, availability, effective daily quota and personal overrides with transactionally recorded before/after values and reason.
- One-use invitations with configurable lifetime and note, redemption identity/time, one-time plaintext and unused-invitation revocation.
- Atomic personal and global UTC-day admission limits; persistent settled, failed and uncertain call accounting with nullable upstream token breakdown.
- Overview, day/user usage groups, request filters/details and bounded CSV export with spreadsheet formula neutralization.
- Version-2 SQLite migration and pre-migration snapshot, preserving identities, registration links and ledger values. Runtime Core, Protocol and Adapter source remain outside this change.

The [operator guide](../development/hosted-administration.md) defines current routes, authentication, accounting and query bounds. The [execution boundary](hosted-admin-execution.md) defines the authorized scope.

## Validation

| Area | Completed evidence |
| --- | --- |
| Full regression | `go test -p 1 ./... -count=1 -timeout=8m` passed. Subsequent directly affected changes passed accounts/hosted focused checks and the final race run. The later oversized-export test also passed independently. |
| Race detector | Accounts and hosted suites passed under `-race` using the existing portable GCC toolchain. |
| Migration | Version-1 data upgrades with a version-2 snapshot, unchanged user/invitation identity and retained unsettled amounts. Unrecorded metadata remains unknown. Existing version-0 migration tests also pass. |
| Admission/accounting | Concurrent requests across accounts cannot exceed the configured global cap; personal overrides, zero limits and restored inheritance take effect. Audit failure rolls back a quota change. Restart retains unknown reservations. Missing token breakdown remains null; inconsistent totals cannot settle. |
| HTTP boundaries | Independent session scopes, cookie path/security attributes, CSRF, current-role revocation, ordinary-user rejection, management route assets and read-only worker inspection pass. |
| Usage/export | Filtered report and export data agree; CSV neutralizes a formula-like email. A 5,001-row match returns a typed error before CSV output. Date ranges, user identifiers and request status filters are bounded. |
| Frontend | Type checking, response-ordering tests and production build pass. The administration bundle is a distinct entry from local/player assets. |
| Browser | Built assets ran against real localhost HTTP and SQLite with isolated test accounts and fake workers/model ledger entries. Login, user search, quota save, rejected mutation with retained inputs, account disable/enable, confirmation focus, invitation lifetime/note/create/hide/revoke, status filtering and actual CSV download were exercised. |
| Failure recovery | Injected 503 reads retained rows with an explicit stale-data message; refresh recovered. Invalidating the real administrator session then refreshing cleared the management view and returned to `/admin/login`. A delayed post-save list response preserved a subsequently selected different user. The player login remained available at `/`. |
| Visual acceptance | Thirteen final screenshots cover six surfaces at 1440px/390px plus the user page at 320px. At 320px the document width remained 320px. Wide tables scroll internally with a visible hint. The independent Impeccable finish reviewer returned `ship` with no material fixes. |
| Design documentation | The documenter created scoped administrator DESIGN/sidecar files and verified the existing player design files remained unchanged. The single detector run had advisory palette/type/radius differences for the deliberately separate administrator identity and no primary finding. |
| Build/contracts | Linux amd64 hosted/worker cross-builds, architecture/Protocol checks, deployment JSON parsing, documentation link checks and `git diff --check` passed. |

Backend CR was performed by the implementing agent. The independent review covers interface source/screenshots; it is not an independent backend security audit. Local screenshot artifacts are under `.impeccable/review/admin-console/` and contain synthetic test accounts.

## Operational boundaries

The service remains a single controller with SQLite and per-account Runtime volumes. Reports describe admitted requests, not rejected pre-admission attempts or currency invoices. Global limits come from deployment configuration; personal limits belong to account records. User grouping is bounded to the highest-occupancy 100 users with explicit truncation feedback; exact user filtering remains available. A usage report is one snapshot, while later refresh/export may include newer activity.

No target Linux/Docker host, public domain or real SMTP receipt acceptance was supplied for this work. No game was started, player save modified or Mod installed. This change does not alter gameplay or establish public-service readiness. See the [deployment release gate](../../deploy/hosted/README.md#updates-and-release-gate).
