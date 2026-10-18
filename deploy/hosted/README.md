# Hosted invitation beta

This deployment runs the WIA account service, authenticated gRPC gateway, private model proxy, and one independent Runtime container per account on one Linux amd64 host. Players run Stardew Valley locally. The platform supplies the model credential.

The source includes local account, TLS gateway, isolation, quota, and browser checks. Linux Docker execution, public DNS/TLS, real-network recovery, capacity, and a complete volume restore require validation on the deployment host before inviting players.

## Prerequisites

- Linux amd64, Docker Engine and Docker Compose, persistent SSD storage, and outbound access to the selected model endpoint.
- Two DNS names on one domain: a website and a game gateway. Point both to the host. Publish ports 80 and 443; keep worker ports, port 8081, and the Docker socket private.
- A platform DeepSeek credential and one model verified with WIA tool calls. The first hosted worker uses the existing DeepSeek provider.
- A durable random account secret of at least 32 bytes, stored separately from backups. Keep its value across updates and restores.
- An SMTP service with certificate-verified implicit TLS or STARTTLS, a sender address authorized by that provider, and an application password. Real delivery must be checked by the operator.
- Off-host backup storage and operator alerts for service failure, disk use, model errors, and spending. Determine the applicable domain/service registration requirements for the chosen region.

The example uses three account workers, 512 MiB per worker, one CPU per worker, and two global model calls. These values are configuration examples, not a tested capacity claim. Retained workers occupy their slots until operator maintenance; automatic idle eviction is not included.

## Build and configure

From the repository root, choose an image tag that identifies the reviewed source:

```sh
docker build -f deploy/hosted/Dockerfile --target worker --build-arg VERSION=YOUR_VERSION -t wia-worker:YOUR_COMMIT .
docker build -f deploy/hosted/Dockerfile --target hosted -t wia-hosted:YOUR_COMMIT .
cd deploy/hosted
cp hosted.example.json hosted.json
cp .env.example .env
```

Set real values in both files. `origin` must exactly match the website origin, and `gateway_url` must match the gateway hostname. Set the commit-tagged worker image, model name, context/output budgets, daily token limit, account capacity, and timeouts. The private model URL remains `http://hosted:8081/v1` with the supplied Compose network.

Generate the account secret with a cryptographic random generator, for example `openssl rand -base64 32`, and retain it as `WIA_ACCOUNT_SECRET`. Set `WIA_MODEL_KEY` only on the hosted controller. Worker containers receive separate internal credentials.

Set `mail.host`, `mail.port`, `mail.tls_mode`, `mail.from` and `mail.username` for the chosen sender. Use `tls` for implicit TLS, commonly port 465, or `starttls` for a server advertising STARTTLS, commonly port 587. Set `mail.password_env` to `WIA_SMTP_PASSWORD` and its value in the private `.env`. The account login email does not determine the sender. With the entire `mail` object omitted, the service can start, but registration and mailbox verification report that mail is unconfigured. There is no production test-code bypass.

```sh
docker compose up -d
docker compose logs --tail 100 hosted ingress
```

Caddy obtains and renews the public certificates. Validate the actual TLS gRPC stream through the gateway hostname; an HTTPS page loading alone does not prove game connectivity. The worker data directories are Docker volumes named `wia-usr_<id>-data`; the controller database is in `wia-hosted-state`.

## Accounts and device keys

Create a one-use invitation without stopping the service:

```sh
docker compose exec -T hosted /usr/local/bin/wia-hosted -config /etc/wia/hosted.json -command invite -invite-hours 72
```

Share the resulting invitation through the operator's chosen channel. A player requests a six-digit code and registers with the invitation, email, password and code. Codes expire after ten minutes, permit five incorrect attempts, and have a sixty-second send cooldown. Successful SMTP acceptance activates a code. Registration consumes the invitation and code in the same transaction. Normal login uses email and password.

The website supports password recovery for verified, enabled accounts. A reset link expires after thirty minutes and is consumed only when a new password is submitted. It revokes previous sessions and WIA Keys while retaining the user's data. Recovery requests return a uniform acceptance response, which does not confirm sending or delivery; the operator must verify actual receipt.

The account service generates an immutable `user_id`. Its independent worker volume contains the existing game/world/NPC namespaces. Email, game IDs, and client-provided owner fields do not choose another account's storage. Passwords use versioned Argon2id hashes. Web sessions, device keys, and internal worker credentials have separate purposes.

Players create WIA Keys on the website. A key's plaintext is displayed once, and first admission binds it to the Mod installation's device ID. Revocation closes the corresponding stream and cancels its active model request. Another device is rejected while an account connection is active. A new connection from the same installation retires the previous connection before forwarding messages.

Private account recovery reads the replacement password from standard input:

```sh
docker compose exec -T hosted /usr/local/bin/wia-hosted -config /etc/wia/hosted.json -command reset-password -email player@example.com
```

Provide the password through stdin and end input. This operation revokes the account's web sessions and device keys. Disable an account with `-command disable -email player@example.com`. These commands are available only to the deployment operator, not through public account APIs.

## Administrator setup

Register and verify the intended administrator account, then grant its role through the private CLI:

```sh
docker compose exec -T hosted /usr/local/bin/wia-hosted -config /etc/wia/hosted.json -command grant-admin -email YOUR_VERIFIED_ADMIN_EMAIL
```

The account must already be enabled and mailbox-verified. The role is not assigned automatically to the first registrant or to an email string in configuration. Refresh or sign in again to see **内测管理**. Revoke it with `-command revoke-admin -email YOUR_VERIFIED_ADMIN_EMAIL`; subsequent requests check the current stored role.

The administrator can create one-use 72-hour invitations, copy a newly created invitation once, revoke an unused invitation, inspect paginated users, disable/enable accounts, and read per-user instance/connection state, UTC usage and audit entries. Status queries do not allocate or start workers. The website excludes player conversation bodies, role editing, database operations and manual release of uncertain quota reservations. An administrator cannot disable their own account through this interface. Enabling an account retains previous credential revocations.

## Account upgrades

Account schema version 1 is migrated transactionally. An existing version-0 database is backed up beside the database as `accounts.sqlite.pre-v1-<timestamp>.sqlite` before the migration. Copy maintenance backups off-host; a same-volume migration snapshot does not provide host-loss recovery.

Existing users keep their IDs, volumes and data. Their mailbox verification time starts empty. Password login leads to a restricted verification screen before ordinary website access or new key creation. Existing WIA Keys retain their expiration/revocation lifecycle. Verification rotates the current web session. Preserve `WIA_ACCOUNT_SECRET` across the upgrade and stop the controller before replacing its image.

## Operator email acceptance

1. Configure the SMTP sender and application password in private deployment files. Confirm its sender-domain requirements with the provider.
2. Create an invitation and register a test mailbox. Confirm the received code, expiry feedback and successful registration; resend must invalidate the earlier code.
3. Sign out, then sign in using only email and password. Confirm mailbox verification is not requested on every login.
4. Request a password reset. Check the inbox and spam folder, open the link, and submit a new password. Merely opening the link must leave it usable.
5. Confirm old password, sessions and keys fail; sign in using the new password and issue a replacement key. Existing identity, preferences and game records must remain.
6. Exercise an invalid/expired link and a sender failure using an isolated test configuration. No response or log should contain the SMTP password, verification code, reset token or model credential.

## Player installation

Build the Stardew Adapter from its own repository with its explicit Protocol dependency. Its release script accepts `-HostedAddress https://YOUR_GATEWAY`; this places the service address and an empty personal key in the package, without distributing a shared credential.

Players install SMAPI and the matching WIA Mod into `Mods`, launch the game, press F8, select online mode, paste their WIA Key, and save. They do not run a local Runtime or provide an LLM key. The website shows connection state, saved tasks, recent turns, explicit preferences, and usage. The Mod retains local connection mode.

## Quota and failure semantics

- Usage periods use UTC calendar days. Before a model call, the controller reserves the configured context window as a conservative token ceiling. Reliable upstream usage settles the call and releases the unused reservation.
- Failed, cancelled, or uncertain upstream calls retain their reservation. They are not automatically replayed. A supplied platform request ID can execute once.
- Account and global model concurrency are bounded. An exhausted quota does not delete memories or mark a game action as successful.
- Explicit nickname, language, and response-length preferences are account data, projected separately from NPC definitions. Workers refresh them within approximately five seconds; a temporary read failure retains the last confirmed values.
- Network failures use bounded exponential backoff in the Mod. Invalid credentials, unsupported versions, and another active device wait for configuration changes. Native game interaction remains available when the connection is unavailable.
- Memory and Task data survive service restarts when their account volumes are retained. Ordinary in-flight Action continuation is process-local and is not resumed after a crash. Existing ordinary-dialogue rollback limitations still apply.

## Backup and recovery

Run a maintenance backup to a new directory:

```sh
bash backup.sh /absolute/path/to/new-backup-directory
```

The script stops the controller, confirms that managed workers have stopped, archives the controller volume and every account volume, writes checksums, and starts the controller again. A failure leaves the service stopped for inspection. Copy the backup off the host and retain the matching deployment files, source/image versions, and account secret separately. Live SQLite files must not be copied individually as a substitute for this closed-volume backup.

For an isolated restore drill on another Docker host:

1. Verify the archive checksums and prepare the matching controller/worker images and deployment configuration.
2. Keep the service stopped. Create empty `wia-hosted-state` and the account volumes listed in `volumes.txt`, applying each account volume's saved `wia.managed`, `wia.deployment` and `wia.user` labels from its volume JSON. Refuse to overwrite existing volumes.
3. Extract `controller.tgz` into the controller volume and each account archive into its exact original volume. Preserve the archive ownership inside the Docker volumes. Use the same account secret; `deployment-id.txt` identifies this deployment's labels.
4. Start the controller and connect test accounts. Verify account IDs, key validity, quota reservations, memory, Task/checkpoint references, and account separation.
5. Test a save whose checkpoint is absent from the restored backup. It must report recovery blocked rather than initialize a replacement history.

Missing previously assigned account volumes block worker startup. Database corruption or incomplete restores require operator recovery; the controller does not replace them with empty data.

## Updates and release gate

Stop the controller and drain workers before updating. Back up data, remove only stopped worker containers after verifying their names, retain their named data volumes, and switch to the reviewed commit-tagged images. The controller checks worker image and ownership before reuse. Roll back images only when their database formats are compatible with the retained data, or restore a matching backup on an isolated host.

Before admitting invited players, record:

- A clean player machine using only SMAPI, Mod, and WIA Key over the actual public gateway.
- Two accounts with identical game/world/NPC identifiers, independent histories, tasks, preferences, keys, and quotas.
- Gateway and worker restarts, revoked/expired keys, connection loss, save/load, appointment success/expiry, and uncertain action results.
- Full closed-volume backup and isolated restore, capacity on the chosen host/model, certificate renewal, operator alerts, and a release rollback drill.

CPU, memory and process counts are configured per worker. Disk alerts and backup retention are host operations; per-account filesystem quotas are not enforced by this implementation. Public self-registration, payments, multiple active devices, RimWorld hosting, and offline game simulation are outside this invitation beta. Local SMTP protocol tests and browser fixtures do not establish real mailbox delivery. See the [email/admin acceptance record](../../docs/phase11/phase11.2-email-admin-acceptance.md).
