---
title: CLI
description: 'Every goliash command: server, agents, targets, users, tokens, matrix, history, inventory and more.'
---

The server binary is also its command-line tool. It works directly on the database, so run it where the server
runs, for example with `docker compose exec goliash goliash …` or `kubectl exec deploy/goliash -- goliash …`.

```text
  goliash [serve] [flags]                 run the server
  goliash env create -name NAME [-position N]
  goliash agent create -name NAME         prints the agent token once
  goliash agent list                      agents with status, version and last contact
  goliash agent rotate -name NAME         new token; the old one works until the agent uses the new one
  goliash agent revoke -name NAME         every token of the agent stops working
  goliash enroll create -env NAME [-many] [-expires 168h|never]
                                          prints an enrollment code once: agents started with it register themselves
  goliash enroll list                     enrollment codes with their environment and agents
  goliash enroll revoke -id ID            no agent can enroll with the code any more
  goliash target create -agent NAME -env NAME -platform PLATFORM -name NAME [-settings JSON] [-poll SECONDS]
                                          PLATFORM: kubernetes, ecs, nomad, swarm, docker, compose, host, lambda,
                                          rds, elasticache, eks, opensearch, msk or manual
  goliash matrix [-at 2026-09-12T14:00]   service × environment versions, now or as of a time
  goliash inventory [-at T] [-environment E] [-format table|csv|cyclonedx]
                                          every running container with image and digest (audits, SBOM)
  goliash hygiene                         moving tags, retagged images, untrusted registries, missing digests
  goliash images SERVICE                  its running images: size against budget, base, signed, SBOM, provenance, last layer change
  goliash licenses [-class attention|all|CLASS] [-environment E] [-os] [-fail-on CLASSES]
                                          licenses of packages in running images, copyleft and unknown first;
                                          -fail-on network-copyleft,source-available exits 1 when one runs
  goliash login [-no-browser] URL         sign the CLI in through the browser (OAuth, read only) for ask and mcp
  goliash logout [URL]                    end that sign-in
  goliash mcp [-server URL] [-token T]    MCP server on stdin/stdout for AI assistants (env GOLIASH_URL, GOLIASH_TOKEN)
  goliash ask [-server URL] [-token T] [-i] QUESTION
                                          ask Goli, the assistant; the answer streams to stdout (env GOLIASH_URL, GOLIASH_TOKEN);
                                          -i keeps the conversation going
  goliash verify [-server URL] [-token T] [-service S] [-sbom FILE] [-fail-on RULES] [-json] IMAGE
                                          in CI, before a deploy: exits 1 when the image carries a vulnerability
                                          exploited in the wild, is past its end of life or is not signed as its
                                          service's policy wants (env GOLIASH_URL, GOLIASH_TOKEN)
  goliash events [-service NAME] [-env NAME] [-since 2h] [-limit N]
  goliash drift                           open drifts
  goliash promotions                      versions waiting for the next environment, with their releases
  goliash delivery [-window 720h]         deploys per environment and lead times between environments
  goliash check [-service NAME]           check upstream registries now
  goliash service set -name NAME [-upstream REPO] [-owner O] [-app A] [-kind own|third_party|auto]
                      [-track patch|minor|major] [-pin-major N] [-tag-filter REGEXP] [-prerelease]
  goliash channel create -type TYPE -name NAME [-url URL] [-secret S] [-token T] [-chat-id ID] [-to a@b,c@d]
                         [-smtp-addr HOST:PORT -smtp-from ADDR [-smtp-username U] [-smtp-tls starttls|tls|none]]
                         [-project P] [-repo OWNER/NAME] [-team T] [-teams MAP] [-labels L] [-email E]
                         [-issue-type T] [-story-type T]
                         TYPE: slack, teams, gchat, discord, telegram, ntfy, grafana, pagerduty, opsgenie, webhook,
                         email, push, inapp, or a ticket tracker: gitlab_issues, github_issues, jira, linear, shortcut
  goliash channel test -name NAME
  goliash badges reset                    invalidate every badge address handed out
  goliash service rename -name NAME -to NAME [-merge]   rename a service, or join it with the one named
  goliash app rename -from NAME [-to NAME] show an application under another name (an existing one merges)
  goliash team rename -from NAME -to NAME   rename a team on all its services
  goliash notify create -channel NAME [-events new_release,drift_detected] [-mode instant|daily|weekly]
                        [-services a,b] [-owners x] [-apps webshop] [-envs prod] [-min-jump minor] [-digest-hour 8]
                        [-timezone Europe/Bratislava] [-quiet 22-7]
  goliash ack -service NAME -kind release|drift [-until-version 2.1.0] [-for 336h] [-env prod]
  goliash workspace create -name N -slug S [-envs]   a workspace per client or team
  goliash workspace list
  goliash user grant -email E -role viewer|member|admin|none   access to the -workspace
  goliash user create -email E [-role owner|admin|member|viewer] [-name N]
  goliash user password -email E [-remove]   set a password (asked for, or one line on stdin)
  goliash user 2fa -email E -reset        turn two-factor sign-in off (lost phone, no recovery codes)
  goliash login-link -email E             one-time sign-in link (creates the first user as owner)
  goliash token create -name N [-role viewer|member] [-expires 90d]   API token for /api/v1, /metrics and /mcp (shown once)
  goliash token list
  goliash token revoke -name N
  goliash rule create -match image_repo|workload_name|label|ignore -pattern REGEXP [-service NAME] [-priority N]
  goliash backup -out DIR                 SQLite: a consistent copy of the database (and goliash.key) while the server runs
  goliash healthcheck                     exit 0 when the local server answers /healthz (container health checks)
  goliash try [-listen :8080] [-public] [-reset 1h]
                                          a throwaway server with example data and a link that signs you in;
                                          -public makes it a read-only demo for everyone, -reset restarts it fresh
  goliash scan [-kube-context C] [-docker-host H] [-no-kubernetes] [-no-docker] [-env prod]
                                          what runs here and what is behind, with nothing installed: reads the current
                                          Kubernetes context and the local Docker, read-only, in a throwaway server
  goliash demo                            fill the workspace with three weeks of example data
  goliash version

Commands that work on the database take -database (env GOLIASH_DATABASE_URL, default goliash.db) and
-workspace SLUG (env GOLIASH_WORKSPACE, default: the first workspace). ask, mcp, verify, login and
logout talk to a server instead, and try makes a database of its own.
```

Changes made with the CLI are recorded in the audit log like changes in the UI.

## Ask Goli from a terminal

`goliash ask` works from anywhere, not on the database: it asks a server's [Goli](/guide/goli/) and prints the
answer as it is written. What Goli looks at goes to stderr, so the answer alone can be piped into a ticket, a commit
message or a chat.

Sign in once through the browser; no token to create or copy:

```sh
goliash login https://goliash.example.com   # opens the browser: sign in, Allow
goliash ask "what changed in prod in the last two hours?"
goliash ask "brief me on upgrading postgres in prod to 17" > postgres-17.md
goliash ask -i                  # a conversation: ask, then ask on; an empty line or Ctrl-D ends it
goliash logout                  # ends the sign-in, on the server too
```

`goliash login` signs the CLI in with the server's OAuth, as you and read only: it shows under **Your account →
Apps with access** like any app. The tokens are kept in your config directory (`~/.config/goliash/credentials.json`
on Linux, `~/Library/Application Support/goliash/` on macOS; `GOLIASH_CREDENTIALS` moves it), readable by you
only, and renewed as they run out; the last server signed in to is the default. On a machine without a browser,
`-no-browser` prints the address to open elsewhere (the answer still comes back to this machine's loopback, so
open it on the same machine, e.g. through SSH port forwarding).

Scripts and CI use an API token instead, which wins over a sign-in:

```sh
export GOLIASH_URL=https://goliash.example.com GOLIASH_TOKEN=glsh_api_…   # goliash token create -name cli
```

Goli sees what you, or the token, may see. Scripts and bots can do the same with `POST /goli/ask` and
`Authorization: Bearer glsh_api_…`: the body is `{"history": [{"role": "user", "text": "…"}]}`, and the answer
comes back as server-sent events (`text`, `tool`, `done`, `error`). The hourly and daily limits count per person or
token.

## Examples

```sh
goliash env create -name staging -position 20
goliash enroll create -env prod                         # prints a code for one agent once
goliash agent create -name prod-eu                      # prints the token once
goliash target create -agent prod-eu -env prod -platform kubernetes -name prod-eu-1 \
  -settings '{"kubernetes":{"exclude_namespaces":["kube-system"]}}'
goliash matrix
goliash events -service payments-api -limit 20
goliash service set -name postgres -track minor -pin-major 15
goliash ack -service postgres -kind release -until-version 17.0
```

## Scan: your own data, nothing installed

`goliash scan` answers "what runs here, and what is behind?" in about a minute, before you set anything up. It
reads the current context of your kubeconfig and the local Docker (`DOCKER_HOST` or the socket), read-only, in a
throwaway server on `127.0.0.1:8080`, and prints what it found:

```text
$ goliash scan

  Reading the Kubernetes context shop and Docker (read-only; nothing is installed or changed) …

  4 services run, 4 compared with their upstream, 4 behind.

  SERVICE    BEHIND        VERSION
  nginx      end of life   1.25.3, cycle 1.25 ended 2024-05-29
  postgres   major         15.6 → 18.7
  traefik    minor         v3.1.2 → 3.7.19

  Everything else — vulnerabilities, history, pull requests — is at

  http://localhost:8080/auth/magic?token=…
```

The link opens the whole of Goliash on what was found: the matrix, Updates, vulnerabilities as the images' SBOMs
are read. Nothing is written to the cluster or the Docker host, and nothing is kept when you stop it with Ctrl-C.

| Flag | Default | |
|---|---|---|
| `-kube-context` | the current context | The kubeconfig context to read |
| `-docker-host` | `DOCKER_HOST`, else the local socket | The Docker API to read |
| `-no-kubernetes`, `-no-docker` | | Leave one out |
| `-env` | `prod` | The environment what is found is shown as |
| `-listen` | `127.0.0.1:8080` | Where the throwaway server listens: local only, as it shows your real data |
| `-wait` | `2m` | How long to wait for the first results before printing what is known |
| `-once` | | Print every row and stop, without the server to look around in |
| `-fail-on` | `eol` | With `-once`, exit with status 3 when something is past end of life (`eol`), a major release behind or past end of life (`major`), behind at all (`any`), or never (`none`) |

### In CI

`-once` makes it a check: it prints everything behind, stops, and fails when something runs past its end of life.
A job with access to the cluster, for example:

```yaml
- name: Nothing past end of life in the cluster
  run: |
    curl -fsSL https://goliash.dev/install.sh | sh
    ~/.local/bin/goliash scan -once -no-docker -kube-context prod
```

Exit status 3 is what was found (status 1 is a scan that could not run). `-fail-on major` is stricter, `-fail-on none`
only reports.

To keep watching, with history and notifications, see [Getting started](/getting-started/).

### Install the command

On Linux and macOS:

```sh
curl -fsSL https://goliash.dev/install.sh | sh
```

[The script](https://github.com/pipozzz/goliash/blob/main/website/public/install.sh) downloads the latest release's
archive for your system, checks it against the release's checksums and puts `goliash` into `~/.local/bin`, without
sudo. `GOLIASH_INSTALL_DIR` names another directory and `GOLIASH_VERSION` another release. For Windows, or to do it
by hand, the archives are on the [releases page](https://github.com/pipozzz/goliash/releases).
