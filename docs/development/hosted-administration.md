# Hosted administration

The hosted controller serves a separate Chinese administrator website at `/admin/login`. After password authentication, `/admin` opens the overview. The player website remains at `/` with its existing game interface.

## Administrator identity and sessions

Initialize a new operator-owned administrator identity through the private CLI:

```sh
docker compose exec -T hosted /usr/local/bin/wia-hosted -config /etc/wia/hosted.json -command init-admin -email YOUR_ADMIN_EMAIL
```

Supply the password through standard input and end input. The password must contain 12–128 characters. This command creates a new account, records the operator's identity assertion and administrator initialization, and rejects an existing email; it does not replace its password or identity. It does not send email. The intended administrator email and the SMTP sender are independent.

For an existing enabled, mailbox-verified account, use `-command grant-admin -email YOUR_ADMIN_EMAIL`. Revoke the role with `-command revoke-admin`. Private password recovery remains available through `reset-password`.

The website provides password login only. Administrator sessions have a separate `wia_admin_session` cookie scoped to `/api/admin`, a stored `admin` scope, CSRF validation and an expiry capped at eight hours. A player session cannot authorize administration, and an administrator session cannot authorize player APIs. Each request reads the account's current status and role. Password replacement and account disable revoke both session types. Login is audited.

## Management pages

| Page | Available operations |
| --- | --- |
| Overview | Account, verified/enabled and active gateway-connection counts; today's global limit, settled use, unsettled reservations, admitted requests, failed/uncertain calls and recent seven-day usage. Connection count does not establish a loaded or ready game world. |
| Users | Search email or ID, inspect verification, creation time, invitation source, effective daily limit, today's use/reservations and lifetime settled use; set a personal limit or restore inheritance, and enable/disable an account. Instance inspection does not allocate or start a worker. |
| Invitations | Create a one-use invitation with a 1–720 hour lifetime and optional 120-character note; show plaintext once, inspect redemption email/time, and revoke an unused invitation. |
| Usage audit | Filter by inclusive UTC dates, exact user ID and request status; inspect totals, day/user groups and paginated request details; export matching calls to CSV. |
| Operation log | Inspect actor, target, action, result and quota-change details. Personal limit changes require a reason and atomically record previous/next values. |

An invitation records registration origin. Its redemption relationship is read from the invitation record; it is not a recurring authorization or quota setting. Revoking an unused invitation prevents registration. An existing account is controlled through its account status. Enabling an account preserves earlier credential revocations. The administrator website rejects self-disable.

## Token accounting and admission

`daily_tokens` is the default per-account limit. A nullable account override chooses a personal limit: `null` inherits the default; zero prevents new model requests. Supported overrides range from zero to 1,000,000,000,000 tokens. A change requires a reason of 1–200 characters and affects later admission, preserving prior calls and reservations.

`global_daily_tokens` sets a positive whole-service daily cap, up to 100,000,000,000,000 tokens. For configuration compatibility, an omitted or zero value derives the cap as `daily_tokens × max_workers`. The overview reports the effective cap. Configuration changes take effect after a controller restart. Set the explicit production cap before opening invitations.

Both limits use UTC calendar days. In one account-store transaction, admission checks the effective personal limit and global occupied amount, then inserts the request's conservative context-window reservation. Decreasing a limit below occupied usage leaves no capacity for a later request; it does not rewrite history or cancel an admitted call.

- **Settled use** is the reliable upstream total recorded for completed calls.
- **Unsettled reservations** include active, failed and uncertain calls without a reliable settlement. They remain charged against the admission day's quota, including across restart.
- **Remaining capacity** is the limit minus settled use and unsettled reservations, clamped to zero.

Calls have `pending`, `settled`, `failed` or `uncertain` status. On controller startup, retained pending calls become uncertain. A known upstream failure retains its reservation. Reliable input/output counts are recorded separately; missing counts stay null, and inconsistent usage does not establish a settlement. Request IDs retain the existing duplicate-admission restriction.

The usage ledger records admitted model calls. Authentication, quota and input-policy rejections before admission are not model-call rows. Tokens are not a currency invoice. Pricing, payments and manual release of uncertain reservations are outside this interface.

## Query and export bounds

Administrator lists and call details use pages of 50. Usage filters allow at most 93 inclusive UTC days. User grouping shows the highest-occupancy 100 users and explicitly reports truncation; an exact user filter retrieves another user's data. Totals and call details within a report share one SQLite snapshot. Separate refreshes and exports may observe later activity.

CSV export uses the same date/user/status filter, includes all matching rows up to 5,000, and rejects a larger result before sending a file. It uses UTF-8 with a BOM and neutralizes spreadsheet formula prefixes. No passwords, invitation plaintext, keys, model prompts or dialogue bodies are exported.

## APIs

All management APIs use the administrator session. Mutations additionally require the expected Origin and `X-CSRF-Token`.

| Endpoint | Purpose |
| --- | --- |
| `POST/GET/DELETE /api/admin/session` | Password login, session read and logout |
| `GET /api/admin/overview` | Effective global quota and service overview |
| `GET /api/admin/users?q=...&page=...` | Search and list users |
| `PUT /api/admin/users/{id}/limit` | `{ "daily_limit": null, "reason": "..." }` or an integer override |
| `PATCH /api/admin/users/{id}` | `{ "enabled": false }` or `true` |
| `GET /api/admin/users/{id}/usage` | Current personal quota and usage |
| `GET /api/admin/users/{id}/runtime` | Read-only instance and gateway connection state |
| `GET/POST /api/admin/invitations` | List or create with `{ "hours": 72, "note": "..." }` |
| `DELETE /api/admin/invitations/{id}` | Revoke an unused invitation |
| `GET /api/admin/usage` | `from`, `to`, optional `user`, `status` and `page` filters |
| `GET /api/admin/usage/export` | Matching bounded CSV export |
| `GET /api/admin/audit?page=...` | Management/account audit entries |

## Upgrade and validation

Account schema version 2 adds scoped sessions, personal limits, invitation notes/redemption time, request metadata and audit details. Existing version-0/1 stores are snapshotted before transactional migration. Existing sessions retain player scope. Historical request metadata and redemption times that were never recorded remain unknown; identity, invitation links and charged amounts remain intact.

See the [management acceptance record](../phase11/hosted-admin-acceptance.md) for source/background evidence and the [deployment guide](../../deploy/hosted/README.md) for server configuration and remaining public-service release gates.
