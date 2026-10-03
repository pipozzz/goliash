# Install

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
See [Security](../security.md#verify-a-release) for how to verify them.

## Pick a platform

| Platform | Server | Agent |
| --- | --- | --- |
| Kubernetes | [Helm chart](kubernetes.md) | [Helm chart](kubernetes.md#agent) with a read-only ClusterRole |
| Docker and Compose | [Compose quickstart](docker.md) | [Compose file](docker.md#agent-on-another-host) with docker-socket-proxy |
| Docker Swarm | [Stack file](swarm.md) | [Stack file](swarm.md#agent) with docker-socket-proxy |
| Nomad | [Job](nomad.md) | [Job](nomad.md#agent) with a read-job ACL token |
| Amazon ECS | any of the above, with PostgreSQL | [Terraform module](ecs.md) with a read-only task role |

## Database

SQLite is the default and suits most installations: one file, backed up by copying it while the server is
stopped or with `sqlite3 goliash.db ".backup copy.db"`. For PostgreSQL, set `GOLIASH_DATABASE_URL` to a
`postgres://` URL. Migrations run on start for both.

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

!!! warning "Back up the key with the database"
    Without the key, stored channels cannot be read and must be created again. The server refuses to start when
    its key does not open the stored secrets, rather than failing at the first notification.

## Public URL

Set `GOLIASH_PUBLIC_URL` to the address people use, for example `https://goliash.example.com`. Sign-in links and
cookies depend on it; with `https://`, cookies are marked Secure. Put the server behind a reverse proxy or ingress
that terminates TLS.

All settings are listed in [Configuration](../reference/configuration.md).
