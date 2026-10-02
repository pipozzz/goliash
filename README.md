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

## Trying it out

The server, the agent protocol and the agent work; collectors for the orchestrators are next.

```sh
make build
export GOLIASH_DATABASE_URL=goliash.db       # or postgres://user:pass@host/db

bin/goliash env create -name prod -position 30
bin/goliash agent create -name eu-cluster    # prints the agent token once
bin/goliash target create -agent eu-cluster -env prod -platform kubernetes -name prod-eu-1 \
  -settings '{"kubernetes":{"exclude_namespaces":["kube-system"]}}'
bin/goliash serve -listen :8080

# in another shell
GOLIASH_SERVER_URL=http://localhost:8080 GOLIASH_AGENT_TOKEN=glsh_agent_... bin/goliash-agent -data-dir ./agent-data
```

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
