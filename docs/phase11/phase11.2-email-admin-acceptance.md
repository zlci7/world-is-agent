# Hosted email and administrator acceptance

Date: 2026-10-19

Status: email/account and administrator source, local automated checks, and background browser checks complete. Actual mailbox delivery, Linux Docker deployment, public connectivity, full volume restoration, capacity, and native game acceptance remain pending.

## Delivered behavior

| Area | Current behavior |
| --- | --- |
| Registration | A valid invitation, email, password and six-digit email code create one verified account. Invitation and code consumption are atomic. Ordinary login uses email and password. |
| Existing accounts | Existing IDs and data remain. Unverified users can sign in to verify their mailbox; ordinary account pages and new WIA Keys require verification. Existing keys retain their expiration and revocation rules. |
| Email credentials | Codes expire after ten minutes, allow five incorrect attempts and have a sixty-second resend cooldown. Reset links expire after thirty minutes and are consumed on password submission. Only keyed digests are stored. |
| Password recovery | Verified, enabled users can receive a one-use reset link. Successful reset revokes sessions and WIA Keys, disconnects active access, and preserves account identity and game data. Recovery requests use a uniform response that does not establish sending or delivery. |
| SMTP | Configured implicit TLS or STARTTLS uses normal certificate verification. A challenge becomes usable after SMTP accepts the message. Failed or uncertain delivery attempts do not activate it. No production test-code bypass exists. |
| Administrator access | The private CLI grants or revokes the role of an existing verified, enabled account. Each administrator request checks the current stored role. |
| Administrator website | Chinese invitation creation/revocation, paginated users, disable/enable, read-only instance/connection state, UTC usage and audit. Status reads do not allocate or start a worker. Disabling revokes credentials; enabling does not restore them. Self-disable is rejected. |
| SQLite upgrade | Schema version 1 adds verification, administrator identity, mailbox challenges and audit attribution. Existing version-0 databases receive a pre-migration snapshot and transactional migration. |
| Interface | Existing Chinese pixel farm styling continues across authentication and administration. Narrow tables scroll inside their container; retained data after a failed refresh carries an explicit stale-data message and last-success time. |

Runtime Core, Protocol and Adapter source are unchanged by this extension. SQLite remains the single-controller account store; each account retains its independent Runtime volume.

## Completed validation

| Check | Evidence and scope |
| --- | --- |
| Go regression | Full repository regression passed with `go test -p 1 ./... -count=1 -timeout=8m`. Later audit attribution changes passed the focused accounts and hosted suites. |
| Race detector | The final accounts and hosted suites passed under `-race` with the existing portable GCC toolchain. |
| Mail and account lifecycle | Tests cover migration and backup, persistent cooldown/attempts, expiry, superseded and delayed challenges, atomic registration rollback/concurrent invitation use, one-use reset, session/key revocation, legacy verification and audit attribution. |
| SMTP protocol | Local SMTP fixtures cover implicit TLS and STARTTLS, certificate trust failures, authentication, send rejection, cancellation and final message acceptance. These tests do not deliver internet email. |
| Administration | Tests cover current role checks, ordinary-user rejection, revocation/disable, self-disable rejection, bounded reads and status queries that do not create generations, volumes or containers. |
| Frontend | Type checking, response-ordering checks and production build passed. Background Chromium exercised built assets against real localhost HTTP and SQLite, with a fake sender and in-process test workers. |
| Browser flows | Invitation registration, login, legacy verification, incorrect code feedback, reset-link navigation, confirmation mismatch, successful reset, invitation management, account disable confirmation, user details, audit and failed-read recovery were exercised. |
| Responsive behavior | Desktop, 390px and 320px views were inspected. At 320px the document stays within the viewport; wide administrator tables scroll within their own container. Selecting a user focuses the detail region; detail refresh preserves the reading position. |
| Interface review | An independent finish reviewer scored its three listed corrections resolved and returned `ship` for that correction list. The design documenter checked the extension against the incumbent system. This is not an independent backend security audit. |
| Build and contracts | Linux amd64 hosted and worker cross-builds, architecture/Protocol checks, deployment JSON parsing, documentation links and whitespace checks passed. |

Backend CR was performed by the implementing agent. Browser screenshots are local review artifacts under `.impeccable/review/email-admin/`; they contain isolated test accounts and do not demonstrate public service availability.

## Pending acceptance

| Check | Required evidence | Current condition |
| --- | --- | --- |
| Real mailbox delivery | Received registration/verification code and reset email, resend/expiry behavior, password replacement and sender-failure behavior | SMTP credentials and recipient acceptance are handled by the operator. The intended administrator login is `zlci77@163.com`; it does not define the SMTP sender. |
| Linux Docker | Actual image startup, private network, resource limits, graceful shutdown and retained-volume restart | No usable Docker environment or target server was supplied. Cross-compilation and fake-engine tests do not replace this check. |
| Public ingress | Actual DNS, certificates and bidirectional gRPC through the public gateway | Website/gateway domain configuration is pending. |
| Full recovery | Restore the stopped controller and all account volumes on an isolated host; preserve identity, ownership and checkpoint behavior | A target Docker host and off-host backup location are pending. |
| Native game | F8 setup, dialogue, gift acknowledgement, recent experience recall, appointments, save/load, disconnect and service restart using an isolated save | No usable native input channel or verified copied-save loading path was available. No game was started, Mod installed or player save modified during this extension. |
| Capacity | Measured host/model concurrency, authentication cost, invitation count, token budget, disk alerts and recovery targets | Deployment hardware, private model configuration and operating limits remain to be set. |

The source and background checks support local feature acceptance. Opening invitations requires the corresponding deployment, mailbox and player evidence above. Ordinary in-flight Action continuation remains process-local, and the existing ordinary Memory save-branch limitation remains.

The [execution plan](phase11.2-execution.md) defines scope and release gates. The [deployment guide](../../deploy/hosted/README.md) contains private configuration, administrator commands and operator mailbox checks. The earlier [hosted baseline acceptance](phase11.2-acceptance.md) records the underlying gateway, isolation and Adapter validation.
