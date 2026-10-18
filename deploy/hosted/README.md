# Hosted invitation beta

This deployment runs the WIA account service, authenticated gRPC gateway, private model proxy, and one independent Runtime container per account on one Linux amd64 host. Players run Stardew Valley locally. The platform supplies the model credential.

The source includes local account, TLS gateway, isolation, quota, and browser checks. Linux Docker execution, public DNS/TLS, real-network recovery, capacity, and a complete volume restore require validation on the deployment host before inviting players.

## Prerequisites

- Linux amd64, Docker Engine and Docker Compose, persistent SSD storage, and outbound access to the selected model endpoint.
- Two DNS names on one domain: a website and a game gateway. Point both to the host. Publish ports 80 and 443; keep worker ports, port 8081, and the Docker socket private.
- A platform DeepSeek credential and one model verified with WIA tool calls. The first hosted worker uses the existing DeepSeek provider.
- A durable random account secret of at least 32 bytes, stored separately from backups. Keep its value across updates and restores.
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

Share the resulting invitation through the operator's chosen channel. A player registers with an invitation, email, and a 12–128 character password. Email is the login identifier; this invitation beta does not verify mailbox ownership or send registration email.

The account service generates an immutable `user_id`. Its independent worker volume contains the existing game/world/NPC namespaces. Email, game IDs, and client-provided owner fields do not choose another account's storage. Passwords use versioned Argon2id hashes. Web sessions, device keys, and internal worker credentials have separate purposes.

Players create WIA Keys on the website. A key's plaintext is displayed once, and first admission binds it to the Mod installation's device ID. Revocation closes the corresponding stream and cancels its active model request. Another device is rejected while an account connection is active. A new connection from the same installation retires the previous connection before forwarding messages.

Private account recovery reads the replacement password from standard input:

```sh
docker compose exec -T hosted /usr/local/bin/wia-hosted -config /etc/wia/hosted.json -command reset-password -email player@example.com
```

Provide the password through stdin and end input. This operation revokes the account's web sessions and device keys. Disable an account with `-command disable -email player@example.com`. These commands are available only to the deployment operator, not through public account APIs.

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

CPU, memory and process counts are configured per worker. Disk alerts and backup retention are host operations; per-account filesystem quotas are not enforced by this implementation. Public self-registration, mailbox verification, payments, multiple active devices, RimWorld hosting, and offline game simulation are outside this invitation beta.
