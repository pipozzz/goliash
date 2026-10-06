---
title: Configuration
description: 'Environment variables for the Goliash server: database, public URL, e-mail, OIDC, passwords, registries and more.'
---

Settings come from environment variables; most have a matching flag.

## Server (`goliash serve`)

| Variable | Flag | Default | Meaning |
| --- | --- | --- | --- |
| `GOLIASH_DATABASE_URL` | `-database` | `goliash.db` (`/data/goliash.db` in the image) | SQLite file path, or a `postgres://` URL |
| `GOLIASH_PUBLIC_URL` | `-public-url` | `http://localhost:8080` | The address people use; sign-in links and cookies depend on it |
| `GOLIASH_LISTEN` | `-listen` | `:8080` | HTTP listen address |
| `GOLIASH_SECRET_KEY` | | | 32-byte key (base64 or hex) that encrypts channel secrets |
| `GOLIASH_SECRET_KEY_FILE` | | | File with the key, instead of the variable |
| `GOLIASH_GITHUB_TOKEN` | | | Token for GitHub release lookups; raises the rate limit |
| `GOLIASH_GITLAB_URL` | | | A self-hosted GitLab whose releases may be read, e.g. `https://gitlab.example.com` |
| `GOLIASH_GITLAB_TOKEN` | | | Token for the self-hosted GitLab (else for gitlab.com) |
| `GOLIASH_ALLOWED_REGISTRIES` | | | Comma-separated registries or prefixes images may come from (image hygiene) |
| `GOLIASH_DEBUG` | `-debug` | off | Debug logging |
| `GOLIASH_LOG_FORMAT` | `-log-format` | `text` | `json` for log collectors |
| `GOLIASH_DRAIN` | `-drain` | `0` | On shutdown, answer `/readyz` with 503 this long before closing |
| `GOLIASH_BACKUP_DIR` | `-backup-dir` | — | SQLite: write a backup here at start and every day |
| `GOLIASH_BACKUP_KEEP` | `-backup-keep` | `7` | Backups kept in the backup directory |
| | `-collect` | `true` | Collect targets that have no agent in the server itself |
| | `-upstream-interval` | `1h` | How often public registries are checked |
| | `-keep-snapshots` | `20` | Processed snapshots kept per target |
| | `-eol` | `true` | Report end-of-life release cycles from endoflife.date |

| `GOLIASH_BOOTSTRAP` | | | Bootstrap configuration (YAML or JSON), see below |
| `GOLIASH_BOOTSTRAP_FILE` | | | File with the bootstrap configuration, instead of the variable |

Without a secret key, a SQLite installation creates `goliash.key` next to the database. See
[Install](/install/#secrets-at-rest).

### Bootstrap

A bootstrap configuration makes a deployment come up ready, without manual steps: on every start the server
creates the environments, targets and first owner that do not exist yet. Nothing is changed or removed, so edits
made later in the UI are kept.

```yaml
owner: you@example.com            # until they sign in, every start logs a one-time sign-in link
environments:
  - { name: staging, position: 20 }
  - { name: prod, position: 30 }
targets:
  - name: nomad
    environment: prod
    platform: nomad                # kubernetes, ecs, nomad, swarm, docker or compose
    poll_interval_seconds: 60
    # agent: prod-eu               # empty: the server collects the target itself
    settings:
      nomad: { address: "http://10.0.0.5:4646" }
      credentials_ref: nomad       # resolved from GOLIASH_CREDENTIAL_NOMAD
```

Unknown fields are rejected, so a typo stops the server instead of being ignored.

### Web push

| Variable | Meaning |
| --- | --- |
| `GOLIASH_PUSH_SUBJECT` | Who sends, for push services: an `https://` URL or a `mailto:` address. Default: `GOLIASH_PUBLIC_URL` when it is https |

### E-mail

| Variable | Meaning |
| --- | --- |
| `GOLIASH_SMTP_ADDR` | SMTP relay, `host:port` |
| `GOLIASH_SMTP_FROM` | Sender address |
| `GOLIASH_SMTP_USERNAME`, `GOLIASH_SMTP_PASSWORD` | Optional authentication |
| `GOLIASH_SMTP_TLS` | `starttls` (when offered), `tls` (implicit) or `none`; by default TLS on port 465, STARTTLS elsewhere |

With SMTP, sign-in links can be requested by e-mail and e-mail channels work. Without it, an e-mail channel can
carry its own mail server (see [Notifications](/guide/notifications/#e-mail)); that one is used for the channel's
messages only, not for sign-in links.

### Passwords

| Variable | Meaning |
| --- | --- |
| `GOLIASH_RECOVERY_EMAIL` | Locked out without a shell: every start logs a one-time sign-in link for this person. Remove it once you are back in |
| `GOLIASH_SESSION_TTL` | How long a browser session lasts at most; default `720h` (30 days) |
| `GOLIASH_SESSION_IDLE` | A session unused this long ends; default `336h` (14 days); `0` turns it off |
| `GOLIASH_PASSWORD_LOGIN` | `false` turns password sign-in off (for example when everyone uses OIDC); default on |
| `GOLIASH_TRUST_PROXY` | `true` takes the client address from `X-Forwarded-For`. Set it only behind a reverse proxy, which must set the header |

The client address goes into the session list, the audit log and the sign-in limits: 8 failed passwords for an
address, or 30 from one client, pause sign-in for 15 minutes.

### OIDC sign-in

| Variable | Meaning |
| --- | --- |
| `GOLIASH_OIDC_ISSUER` | Issuer URL, e.g. `https://accounts.google.com` |
| `GOLIASH_OIDC_CLIENT_ID`, `GOLIASH_OIDC_CLIENT_SECRET` | Client credentials |
| `GOLIASH_OIDC_NAME` | Button label, e.g. `Google` |
| `GOLIASH_OIDC_DOMAINS` | Comma-separated e-mail domains whose people get a viewer account on first sign-in |

Redirect URI: `<GOLIASH_PUBLIC_URL>/auth/oidc/callback`.

## CLI

Every command other than `serve` takes `-database` (`GOLIASH_DATABASE_URL`) and `-workspace`
(`GOLIASH_WORKSPACE`, default the first workspace). Flags go after the command: `goliash matrix -workspace acme`.

## Agent (`goliash-agent`)

| Variable | Flag | Default | Meaning |
| --- | --- | --- | --- |
| `GOLIASH_SERVER_URL` | `-server` | | Server URL |
| `GOLIASH_AGENT_TOKEN` | | | The agent's token (`glsh_agent_…`) |
| `GOLIASH_AGENT_TOKEN_FILE` | | | File with the token, instead of the variable |
| `GOLIASH_DATA_DIR` | `-data-dir` | `data` (`/data` in the image) | Buffered snapshots while the server is unreachable |
| `GOLIASH_CREDENTIALS_DIR` | | `/etc/goliash-agent/credentials` | Directory of credential files |
| `GOLIASH_CREDENTIAL_<NAME>` | | | A credential; see [Collectors](/reference/collectors/#credentials) |
| `GOLIASH_COMPOSE_DIRS` | | | Comma-separated directories `compose` targets may read files from |
| `GOLIASH_DEBUG` | `-debug` | off | Debug logging |
| `GOLIASH_LOG_FORMAT` | `-log-format` | `text` | `json` for log collectors |

Everything else (targets, intervals, which registries to check) the agent reads from the server.
