# Goliash

**What runs where, on which version, in which environment — across Kubernetes, ECS, Nomad and Docker Swarm.**

Goliash watches what is actually running, builds a *service × environment* matrix with the running and the latest
upstream version, and tells you when a new release ships or when environments drift apart (staging 1.5.0, prod 1.3.2).

> **Status:** pre-alpha. v0.1 (MVP) is under active development. Nothing here is stable yet.

## Why

- **Runtime is the source of truth.** Goliash reads what really runs. Git and CI are optional extras.
- **Read-only.** Goliash never deploys or changes anything. Every collector needs read-only permissions only.
- **Agent-first.** Collection runs inside your network and sends data out over outbound HTTPS. Credentials never leave.
- **Snapshots, not events.** The agent sends full state; the server diffs it. An agent outage loses no events.
- **Less noise.** Digests, dedup and ack/snooze are part of the core.
- **Simple to run.** One binary, SQLite or PostgreSQL.

Out of scope: deploying or upgrading services (that is CI's or Renovate's job), CVE scanning, library versions in code.

## How it works

```
 your network                                   Goliash server
┌────────────────────────────────┐             ┌──────────────────────────────────┐
│ Kubernetes / ECS / Nomad /     │             │ ingest → mapping → versions      │
│ Swarm  ──read-only──► agent ───┼──HTTPS────► │   │                  │           │
│ private registries ──► agent   │  snapshots  │   ▼                  ▼           │
└────────────────────────────────┘             │ events, matrix   notifier ──► Slack, webhook, e-mail
                                               │ REST API + UI    SQLite / Postgres│
                                               └──────────────────────────────────┘
```

- **goliash-agent** reads orchestrators and private registries and sends snapshots, heartbeats and registry results.
- **goliash** (server) turns snapshots into events, maps containers to services and environments, compares them
  with upstream releases using per-service semver policies, and sends notifications. Self-hosted, the server can
  also run collectors itself, without an agent.

## v0.1 scope

| Area | v0.1 |
| --- | --- |
| Collectors | Kubernetes (watch), ECS, Nomad, Docker Swarm |
| Agent protocol | `register`, `config`, `snapshot`, `heartbeat`, `registry-results` (REST + JSON, OpenAPI) |
| Versions | Snapshot diff → events, service × environment matrix, OCI registry tags with semver policy |
| Notifications | Slack webhook, generic webhook, e-mail |
| Storage | SQLite and PostgreSQL |
| UI | Server-rendered (templ + htmx) |

## Repository layout

| Path | What | License |
| --- | --- | --- |
| `cmd/goliash` | Server binary | AGPL-3.0-only |
| `cmd/goliash-agent` | Agent binary | Apache-2.0 |
| `internal/agent` | Agent loop | Apache-2.0 |
| `internal/collectors/{kubernetes,ecs,nomad,swarm}` | Read-only collectors | Apache-2.0 |
| `internal/registry` | OCI Distribution API client | Apache-2.0 |
| `api/agent-v1.yaml` | Agent protocol (OpenAPI 3.0) | Apache-2.0 |
| `pkg/agentproto` | Protocol types and client, generated from the spec | Apache-2.0 |
| `pkg/buildinfo` | Build metadata | Apache-2.0 |
| `internal/{api,ingest,mapping,versions,notifier,store,ui}` | Server | AGPL-3.0-only |
| `internal/store/migrations/{sqlite,postgres}` | Database migrations (goose), embedded in the server | AGPL-3.0-only |
| `deploy/{helm,nomad,swarm,ecs}` | Deployment manifests | AGPL-3.0-only |

## Building

Requires Go (version in `go.mod`) and, for linting, [golangci-lint](https://golangci-lint.run) v2.

```sh
make build          # bin/goliash and bin/goliash-agent
make generate       # regenerate pkg/agentproto after editing api/agent-v1.yaml
make test           # SQLite; set GOLIASH_TEST_POSTGRES_DSN to also run against PostgreSQL
make lint           # license boundary check + golangci-lint
```

## Quickstart

```sh
docker compose up -d --build
docker compose exec goliash goliash demo                            # optional: three weeks of example data
docker compose exec goliash goliash login-link -email you@example.com
```

Open the printed link. In **Agents**, add environments, an agent (copy its token) and targets, then run the agent
where it can reach your orchestrator:

```sh
docker run -d --name goliash-agent -v goliash-agent:/data \
  -e GOLIASH_SERVER_URL=http://goliash.example.com -e GOLIASH_AGENT_TOKEN=glsh_agent_... \
  ghcr.io/pipozzz/goliash-agent:latest
```

Ready-made manifests for Kubernetes (Helm), Nomad, Docker Swarm and ECS are in [`deploy/`](deploy).

**Without an agent:** a target created without an agent (`goliash target create` without `-agent`, or "the server
itself" in the UI) is collected by the server, with the same read-only collectors. That suits a self-hosted server
next to what it watches, e.g. the Kubernetes cluster it runs in (`helm … --set collectInCluster=true`) or a Swarm
through a socket proxy. Turn it off with `-collect=false`.

## Install

- **Images:** `ghcr.io/pipozzz/goliash` and `ghcr.io/pipozzz/goliash-agent` (linux/amd64, linux/arm64; distroless,
  non-root). Images and release archives are signed with cosign (keyless) and come with SPDX SBOMs.
- **Binaries:** archives for Linux, macOS and Windows on the GitHub releases page.
- **From source:** `make build`, or `docker build --target server .` / `--target agent`.

The server keeps its SQLite database in `/data` (set `GOLIASH_DATABASE_URL=postgres://…` for PostgreSQL) and
deletes processed snapshots beyond the newest 20 per target every hour (`-keep-snapshots`); history lives in events.
Releases are published by pushing a `v*` tag.

### Web UI

Open `GOLIASH_PUBLIC_URL` (default `http://localhost:8080`) and sign in with a link from
`goliash login-link -email you@example.com`. The UI is server-rendered (templ + htmx), works without a build step
and updates live over server-sent events.

| Page | What it is for |
| --- | --- |
| Matrix | service × environment with versions, replicas, targets, drift badges and the latest upstream |
| Service | where it runs, upstream releases, history, version policy, acknowledgements, "check upstream now" |
| Inbox | unmapped workloads with a suggested name; mapping creates a rule for that image |
| History | every event, filterable by service, environment and type |
| Agents | agents and targets with collector health; add agents (token shown once), environments and targets |
| Notifications | channels (with a test button) and rules |
| Users | invite people, change roles, sign-in links, API tokens (admins) |

### Sign-in, API and metrics

```sh
bin/goliash login-link -email you@example.com      # first user becomes owner; prints a one-time link
bin/goliash user create -email dev@example.com -role member
bin/goliash token create -name prometheus           # glsh_api_… for /api/v1 and /metrics
```

- **Magic links** are e-mailed when SMTP is configured (`GOLIASH_SMTP_*`); `goliash login-link` works without it.
- **OIDC**: `GOLIASH_OIDC_ISSUER`, `GOLIASH_OIDC_CLIENT_ID`, `GOLIASH_OIDC_CLIENT_SECRET`, optional `GOLIASH_OIDC_NAME`
  and `GOLIASH_OIDC_DOMAINS` (people from these e-mail domains are created as viewers on first sign-in; others
  need an account). Redirect URI: `<public URL>/auth/oidc/callback`.
- Set `GOLIASH_PUBLIC_URL` to the address people use; with `https://` cookies are marked Secure.
- **Roles**: viewer reads; member maps services, edits policies and acks; admin manages agents, targets, channels,
  tokens and users; owner can do everything.
- **REST API** (session or `Authorization: Bearer glsh_api_…`): `GET /api/v1/matrix`, `/services`, `/environments`,
  `/targets`, `/events?service=&environment=&type=&before=&limit=`, `/drifts`; `POST /api/v1/acks`.
- **Prometheus** `GET /metrics` (same auth): `goliash_deployed_version_info`, `goliash_outdated`, `goliash_drift_days`.

### Versions, upstream and drift

```sh
bin/goliash matrix                 # service × environment, latest upstream, drift markers
bin/goliash drift                  # open drifts
bin/goliash events                 # history: deployed, version_changed, removed, new_release, drift_*
bin/goliash check                  # check upstream registries now (the server does it hourly)
bin/goliash service set -name postgres -track minor -pin-major 15
bin/goliash rule create -match image_repo -pattern 'ghcr\.io/acme/pay.*' -service payments
```

- **Mapping:** labels `goliash.service` / `app.kubernetes.io/name` (and `goliash.env`), then rules, then unmapped
  workloads wait in the inbox with a suggested name.
- **Upstream:** public registries (Docker Hub, GHCR, Quay, registry.k8s.io, …) are checked by the server; other
  registries by the agent, with credentials from `GOLIASH_CREDENTIAL_<REGISTRY_HOST>`. New services are checked
  within a minute, then hourly.
- **Policy per service:** `tag_filter`, `track` (patch/minor/major), `pin_major`, `prerelease`. Without a filter,
  tags are compared like with like (same `-alpine` variant, same number of version parts).
- **Catalog:** [`catalog/images.yaml`](catalog/images.yaml) holds default policies for popular public images
  (postgres, redis, nginx, keycloak, traefik, grafana, prometheus, …) and where their release notes live. A service
  without its own policy uses the catalog's; additions are welcome as pull requests.
- **Release notes:** releases get their publication date and a release notes link from GitHub releases (the
  catalog's `github`, or `"github": "owner/repo"` in a service's own policy) or a changelog URL template. Links
  appear in the UI and in new-release notifications. Set `GOLIASH_GITHUB_TOKEN` to raise GitHub's rate limit.
- **Drift:** `env` (an environment runs an older version than the one before it), `upstream` (behind the newest
  release by at least the tracked jump), `inconsistent` (targets of one environment disagree). Drift shows in the UI
  at once but is announced (`drift_detected`) only after it lasts: `env` 7 days, `upstream` immediately,
  `inconsistent` 15 minutes. Override per service, e.g. `"drift_alert_after": {"env": "72h"}`.
- **Stale data:** a target whose agent stopped sending heartbeats, or without a snapshot for three poll intervals
  (at least 15 minutes), is marked "stale data" in the matrix.
- **Audit log:** every change to configuration, tokens and roles (UI, API, CLI) and every sign-in is recorded and
  shown to admins on the Users page.

### Workspaces (MSPs, teams)

Each workspace has its own agents, targets, services, history, notifications and API tokens.

```sh
bin/goliash workspace create -name "Client A" -slug client-a -envs
bin/goliash user create -email ops@client-a.example -role member -workspace client-a
bin/goliash user grant -email ops@client-a.example -role admin -workspace client-a
bin/goliash matrix -workspace client-a        # every command takes -workspace (or GOLIASH_WORKSPACE)
```

Organization owners and admins reach every workspace and switch between them in the top bar. Everyone else only
sees the workspaces they were invited to, as viewer, member or workspace admin; a workspace admin manages the
people of that workspace only. Clients of an MSP therefore never see each other.

### Notifications

```sh
bin/goliash channel create -type slack -name ops -url https://hooks.slack.com/services/…
bin/goliash channel create -type webhook -name ci -url https://example.com/goliash -secret s3cret
bin/goliash channel create -type email -name oncall -to oncall@example.com   # needs GOLIASH_SMTP_ADDR, GOLIASH_SMTP_FROM
bin/goliash channel test -name ops
bin/goliash notify create -channel ops -events new_release,drift_detected,agent_stale -mode daily -min-jump minor
bin/goliash ack -service postgres -kind release -until-version 17.0     # "we know about 16, quiet until 17"
bin/goliash ack -service web -kind drift -for 336h                       # quiet for 14 days
```

Rules match event types and optionally services, owners, environments and the size of a release jump. `instant`
rules deliver within seconds (items arriving together are batched); `daily` and `weekly` rules collect a digest
sent at `digest_hour` (UTC). A release is announced once per service and version. Failed deliveries are retried
with backoff. Webhook bodies are signed: `X-Goliash-Signature: sha256=HMAC(secret, X-Goliash-Timestamp + "." + body)`.

### Collectors and credentials

| Platform | Reads | Needs | Credentials |
| --- | --- | --- | --- |
| Kubernetes | Deployments, StatefulSets, DaemonSets, CronJobs and their running pods (watch) | ClusterRole with `get`, `list`, `watch` | in-cluster service account, else kubeconfig (`kubeconfig_context` optional) |
| ECS | clusters → services → running tasks → task definitions | IAM `ecs:List*`, `ecs:Describe*` | default AWS chain; `credentials_ref` = AWS profile name |
| Nomad | jobs, job versions, allocations | ACL token with `read-job` | `credentials_ref` → token, else `NOMAD_TOKEN` |
| Docker Swarm | services and running tasks | Docker API `GET` only (docker-socket-proxy) | none |

The server only sends a `credentials_ref` name. The agent resolves it from the environment variable
`GOLIASH_CREDENTIAL_<NAME>` (upper-cased, non-alphanumerics as `_`) or the file
`$GOLIASH_CREDENTIALS_DIR/<name>` (default `/etc/goliash-agent/credentials`).

The agent registers, follows its configuration (polled every minute with an ETag), sends a heartbeat every minute
and buffers snapshots in `-data-dir` while the server is unreachable (the oldest are dropped beyond 200).
The protocol is in [`api/agent-v1.yaml`](api/agent-v1.yaml); the server validates every request against it.

## License

The agent and the code it is built from are licensed under [Apache-2.0](LICENSES/Apache-2.0.txt); the server is
licensed under [AGPL-3.0-only](LICENSE). Each source file states its license in an SPDX header.
See [LICENSING.md](LICENSING.md) for details.

## Contributing

Contributions are welcome. Please read [CONTRIBUTING.md](CONTRIBUTING.md); external contributions require signing
the [CLA](CLA.md).
