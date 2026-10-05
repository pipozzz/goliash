---
title: Install
description: 'Pick how to run the Goliash server and agents: Kubernetes, Docker, Swarm, Nomad or ECS, with SQLite or PostgreSQL.'
---

Goliash is two programs:

| | What it does | Where it runs | License |
| --- | --- | --- | --- |
| `goliash` | The server: UI, API, ingestion, version checks, notifications | Anywhere your team can reach | AGPL-3.0-only |
| `goliash-agent` | Reads orchestrators and private registries, sends snapshots | Next to what it watches | Apache-2.0 |

An agent is optional: the server can collect targets it can reach itself.

## Images and binaries

- **Images:** `ghcr.io/pipozzz/goliash` and `ghcr.io/pipozzz/goliash-agent`, for linux/amd64 and linux/arm64.
  They are distroless and run as a non-root user. Tags follow releases (`0.1.0`), and `latest` is the newest
  release.
- **Binaries:** archives for Linux, macOS and Windows on the
  [releases page](https://github.com/pipozzz/goliash/releases).
- **From source:** `make build`, or `docker build --target server .` and `docker build --target agent .`.

Images and release archives are signed with cosign (keyless, from the release workflow) and come with SPDX SBOMs.
See [Security](/security/#verify-a-release) for how to verify them.

## Pick a platform

| Platform | Server | Agent |
| --- | --- | --- |
| Kubernetes | [Helm chart](/install/kubernetes/) | [Helm chart](/install/kubernetes/#agent) with a read-only ClusterRole |
| Docker and Compose | [Compose quickstart](/install/docker/) | [Compose file](/install/docker/#agent-on-another-host) with docker-socket-proxy |
| Docker Swarm | [Stack file](/install/swarm/) | [Stack file](/install/swarm/#agent) with docker-socket-proxy |
| Nomad | [Job](/install/nomad/) | [Job](/install/nomad/#agent) with a list-jobs and read-job ACL token |
| Amazon ECS | any of the above, with PostgreSQL | [Terraform module](/install/ecs/) with a read-only task role |

## Managing agents

Create an agent under **Settings → Agents and targets** (or `goliash agent create -name prod-eu`). Its page shows the token once,
with commands to start the agent on a Docker host, Kubernetes, Nomad or as a binary, and then:

- **Status**: online, stale (missed heartbeats), never connected or revoked, its version (with *update available*
  when it is older than the server), host, platforms and the targets it collects.
- **Rotate token** without a gap: the old token keeps working until the agent first connects with the new one, then
  stops. Rotating an agent that never connected replaces its token at once. CLI: `goliash agent rotate -name N`.
- **Revoke** every token when one leaked or the machine is gone; the agent stops sending data at once.
  CLI: `goliash agent revoke -name N`.
- **Rename** (the token stays) and **delete** an agent once it has no targets.
- **Move a target** to another agent or to the server, or delete it. The agents pick the change up with their next
  configuration poll. Deleting a target removes its running versions from the matrix; the history stays.

`goliash agent list` prints every agent with its status, version and last contact.

## Database

SQLite is the default and suits most installations: one file. For PostgreSQL, set `GOLIASH_DATABASE_URL` to a
`postgres://` URL. Migrations run on start for both.

### Backups

**SQLite** backs up while the server runs: `goliash backup -out /backups` writes a consistent copy
(`goliash-<time>.db`) and, when the secret key is the file next to the database, `goliash-<time>.key` beside it. Or
let the server do it: `GOLIASH_BACKUP_DIR=/data/backups` writes one at start and every day after, keeping
`GOLIASH_BACKUP_KEEP` (default 7). Put the directory on other storage, or copy it off the machine.

To restore, stop the server, put the copy in place as `goliash.db` and its key as `goliash.key` (or keep
`GOLIASH_SECRET_KEY`), and start it.

**PostgreSQL** is backed up with its own tools: `pg_dump -Fc "$GOLIASH_DATABASE_URL" > goliash.dump`, or your
provider's snapshots. Keep `GOLIASH_SECRET_KEY` with them.

### Upgrades

Replace the image or binary and start it: migrations run on start, under a lock when several servers share
PostgreSQL. Take a backup first. Every change is tested by upgrading a database written by the first release and by
the latest release, on SQLite and PostgreSQL, and checking that its services, history and encrypted channel secrets
survive (`scripts/upgrade-test.sh`). Downgrades are not supported: restore the backup instead.

### Logs and health

`GOLIASH_LOG_FORMAT=json` writes one JSON object per line for Loki or Elasticsearch (the agent too). `/healthz`
says whether the server runs and reaches its database (liveness); `/readyz` also answers 503 while it shuts down,
so load balancers stop sending first: set `GOLIASH_DRAIN=5s` to wait that long before closing connections (the Helm
chart does). `/metrics` includes the server's own health; see [metrics](/guide/notifications/#metrics-instead-of-messages).

### Several servers

With PostgreSQL, run as many servers as you like behind one address (the Helm chart's `replicaCount`). They all serve
the UI, the API and agents; one of them, the **leader**, also runs the background work: processing snapshots,
checking upstreams, sending notifications and housekeeping. The leader holds a PostgreSQL advisory lock; when it
stops or loses its database connection, another server takes over within seconds. New snapshots and live updates
reach every server through `LISTEN`/`NOTIFY`, so a browser on any server sees changes at once. Migrations run under
a lock, so servers may start together.

Give every server the same `GOLIASH_SECRET_KEY` and `GOLIASH_PUBLIC_URL`. Limits on failed password sign-ins are
counted per server. SQLite allows one server.

The server keeps processed snapshots for the newest 20 per target (`-keep-snapshots`); history lives in events, so
the database stays small.

## Secrets at rest

Notification channel secrets (Slack and webhook URLs, signing secrets) are encrypted with AES-256-GCM. The key comes
from `GOLIASH_SECRET_KEY` or `GOLIASH_SECRET_KEY_FILE`:

```sh
openssl rand -base64 32
```

With SQLite and neither set, the server creates `goliash.key` next to the database. With PostgreSQL, set the key
yourself; without one, secrets are stored unencrypted and the server logs a warning.

:::caution[Back up the key with the database]
Without the key, stored channels cannot be read and must be created again. The server refuses to start when
its key does not open the stored secrets, rather than failing at the first notification.
:::
## Public URL

Set `GOLIASH_PUBLIC_URL` to the address people use, for example `https://goliash.example.com`. Sign-in links and
cookies depend on it; with `https://`, cookies are marked Secure. Put the server behind a reverse proxy or ingress
that terminates TLS.

All settings are listed in [Configuration](/reference/configuration/).
