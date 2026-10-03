# Goliash

**What runs where, on which version, in which environment — across Kubernetes, Amazon ECS, Nomad, Docker Swarm
and plain Docker.**

Goliash watches what actually runs, builds a **service × environment matrix** with the running and the newest
upstream version, and tells you when a new release ships or when environments drift apart: staging on 1.5.0 while
prod is still on 1.3.2, or two prod clusters disagreeing.

![The Goliash matrix: services against dev, staging and prod with running versions, drift badges and the latest upstream release](assets/matrix.png)

[Get started](getting-started.md){ .md-button .md-button--primary } [Install](install/index.md){ .md-button }

## What you get

- **One matrix for every orchestrator.** Kubernetes (watch), Amazon ECS, Nomad, Docker Swarm and Docker/Compose
  hosts side by side, per environment, with replicas, targets and rollouts in progress.
- **Upstream awareness.** Tags from Docker Hub, GHCR, Quay, registry.k8s.io, Amazon ECR and other private
  registries, compared with per-service semver policies. A built-in catalog of popular images, and release dates and
  release notes from GitHub, also found through the image's `org.opencontainers.image.source` label.
- **Drift that matters.** An environment behind the one before it, a version behind upstream, targets that
  disagree: shown at once, announced only when it lasts.
- **History without CI.** Every deploy, rollout, retag and removal, read from the runtime itself.
- **Notifications with less noise.** Slack, signed webhooks and e-mail, instant or as daily or weekly digests,
  with deduplication and acknowledgements.
- **Built for teams and MSPs.** Workspaces per client, roles, magic-link and OIDC sign-in, audit log, a REST API
  with an OpenAPI description, and Prometheus metrics.
- **Read-only and easy to run.** Collectors only ever read. The agent sends data out over HTTPS and credentials
  stay in your network. One binary each, SQLite or PostgreSQL.

Out of scope: deploying or upgrading services (that is CI's or Renovate's job), CVE scanning, and library versions
inside code.

## How it works

```
 your network                                   Goliash server
┌────────────────────────────────┐             ┌──────────────────────────────────┐
│ Kubernetes / ECS / Nomad /     │             │ ingest → mapping → versions      │
│ Swarm / Docker ─read-only─► agent ─HTTPS──► │   │                  │           │
│ private registries ──► agent   │  snapshots  │   ▼                  ▼           │
└────────────────────────────────┘             │ events, matrix   notifier ──► Slack, webhook, e-mail
                                               │ UI, REST API     SQLite / Postgres│
                                               └──────────────────────────────────┘
```

- **goliash-agent** runs next to what it watches. It reads orchestrators and private registries and sends full
  snapshots, heartbeats and registry results. It sends snapshots, not events, so an agent outage loses nothing.
- **goliash**, the server, turns snapshots into events, maps containers to services and environments, compares
  them with upstream releases and sends notifications. It can also collect targets itself, without an agent.

## Status

Early. Goliash works end to end and is being tried on real infrastructure; expect changes before 1.0. Issues and
pull requests are welcome on [GitHub](https://github.com/pipozzz/goliash).
