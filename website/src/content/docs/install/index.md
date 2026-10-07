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
  release. The commands on the **Connect** page pin the agent image, chart and files to the server's own release, so
  an agent you add later matches the server instead of whatever `latest` is that day.
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
| Amazon ECS | any of the above, with PostgreSQL | [CloudFormation template or Terraform module](/install/ecs/) with a read-only task role |
| AWS Lambda | any of the above | [the ECS module](/install/ecs/#lambda-functions) with `watch_lambda`, or any agent with AWS credentials |

## Enrollment codes

An enrollment code (`glsh_enroll_…`) lets an agent register itself: start the agent with the code where a token
would go, and it finds what it can read where it runs, becomes an agent named after it and adds its targets to the
code's environment. Nothing has to be set up in Goliash first.

**Connect a cluster or host** (Settings → Agents and targets) does it in two clicks: pick the platform, then *Get
the command*. The command carries a new code; the page then follows the agents that register with it, the targets
they found and their first report. *Set up by hand* below it still creates a target with your own settings, for an
agent you have or for the server.

From the CLI:

```sh
goliash enroll create -env prod              # a code for one agent, valid 7 days
goliash enroll create -env prod -many        # one code for a fleet, e.g. every Docker host
```

| The agent runs | It adds | Named after | Needs |
| --- | --- | --- | --- |
| In Kubernetes | the cluster | `name` in the Helm values, else `kubernetes` | nothing |
| On ECS, or with AWS credentials and `AWS_REGION` | every ECS cluster of its region, each a target of its own | its cluster, else `aws-<region>` | `ecs:ListClusters` (else only its own cluster) |
| In Nomad | the region, through the Nomad agent of its node | `nomad-<region>` | `NOMAD_ADDR` (set by the job file) |
| With a Docker socket | the host, or the swarm on a manager | the host name | `INFO=1` on the socket proxy |
| The same | the region's Lambda functions, as `lambda-<region>` | | `lambda:ListFunctions`, `lambda:GetFunction` |

In AWS, `GOLIASH_AWS_REGIONS` adds the clusters and functions of more regions (their targets end in the region)
and `GOLIASH_ECS_CLUSTERS` keeps only the named clusters. Each cluster lands in the code's environment; move the
staging one to staging on its target page once, and it stays there.

`GOLIASH_AGENT_NAME` sets the name, and `GOLIASH_TARGETS` declares more targets as a JSON array, e.g. Compose
files: `[{"platform":"compose","name":"shop","compose":{"files":["/srv/shop/compose.yml"]}}]`. Targets the agent
added follow what it reports when it starts again; their environment, and anything else, can still be changed in
Goliash.

The agent keeps no state: it enrolls on every start and gets a new token, which ends the old one.

- **A code for one agent** belongs to the first agent that enrolls with it, which can come back with it at any time.
  It works like a token that the agent exchanges for its own.
- **A code for many agents** (`-many`) tells them apart by what identifies their installation: the cluster's CA, the
  ECS cluster, the Nomad job and region, or the Docker engine or swarm ID. Where none is found, set
  `GOLIASH_AGENT_ID` to a name that stays the same across restarts.
- **Expiry** (7 days by default, `-expires 720h` or `-expires never`) stops new agents only: agents that enrolled
  keep coming back with the code. **Revoking** it (`goliash enroll revoke -id ID`) stops every agent from enrolling
  with it again, and **revoking an agent** keeps it out until it is deleted. `goliash enroll list` shows the codes
  and how many agents used each.

The **Enrollment codes** panel on the agents page lists the codes with their environment, how many agents used
them and until when new agents may join, and revokes them. Targets an agent found show *managed by agent*: their
settings come from the agent on every start, while their environment and interval stay yours to change.

Agents created with `goliash agent create` keep their token and their targets set up in Goliash.

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

With PostgreSQL, run as many servers as you like behind one address (the Helm chart's `replicaCount`); the load
balancer needs no sticky sessions. Every server serves the UI, the REST API, MCP and agents. One of them, the
**leader**, also runs the background work: processing snapshots, checking upstreams, sending notifications,
housekeeping and server-side collectors.

- **Failover.** Leadership is a lease in the database that the leader renews every 5 seconds and that lasts 15,
  timed by the database's clock. When the leader crashes, freezes or loses its network, another server takes over
  within about 20 seconds; a leader that comes back after its lease ran out stops its work first. A server that
  shuts down on purpose hands over at once. `goliash_leader` in `/metrics` shows which server leads.
- **Shared state.** Sessions, sign-in tokens, failed sign-in counts, setup links and everything else live in the
  database. New snapshots wake the leader and live updates reach every server's browsers through PostgreSQL
  `LISTEN`/`NOTIFY`. MCP over HTTP keeps no session, so any server answers any request. Migrations run under a lock,
  so servers may start together.
- **What you provide.** Give every server the same `GOLIASH_SECRET_KEY` (a server with another key refuses to
  start rather than misread channel secrets) and the same `GOLIASH_PUBLIC_URL`. The database itself must be highly
  available (a managed PostgreSQL, or Patroni and the like): Goliash keeps no state outside it.

SQLite allows one server.

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
