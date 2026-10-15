---
title: CLI
description: 'Every goliash command: server, agents, targets, users, tokens, matrix, history, inventory and more.'
---

The server binary is also its command-line tool. It works directly on the database, so run it where the server
runs, for example with `docker compose exec goliash goliash …` or `kubectl exec deploy/goliash -- goliash …`.

```text
Usage:
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
  goliash target create -agent NAME -env NAME -platform kubernetes|ecs|nomad|swarm|docker|compose|lambda -name NAME [-settings JSON] [-poll SECONDS]
  goliash matrix                          service × environment versions
  goliash events [-service NAME] [-limit N]
  goliash drift                           open drifts
  goliash inventory [-at T] [-environment E] [-format table|csv|cyclonedx]
                                          every running container with image and digest (audits, SBOM)
  goliash check [-service NAME]           check upstream registries now
  goliash service set -name NAME [-upstream REPO] [-owner O] [-app A] [-kind own|third_party]
                      [-track patch|minor|major] [-pin-major N] [-tag-filter REGEXP] [-prerelease]
  goliash channel create -type slack|webhook|email|push -name NAME [-url URL] [-secret S] [-to a@b,c@d]
                         [-smtp-addr HOST:PORT -smtp-from ADDR [-smtp-username U] [-smtp-tls starttls|tls|none]]
  goliash channel test -name NAME
  goliash notify create -channel NAME [-events new_release,drift_detected] [-mode instant|daily|weekly]
                        [-services a,b] [-owners x] [-apps webshop] [-envs prod] [-min-jump minor] [-digest-hour 8]
                        [-timezone Europe/Bratislava] [-quiet 22-7]
  goliash badges reset                    invalidate every badge address handed out
  goliash service rename -name NAME -to NAME [-merge]   rename a service, or join it with the one named
  goliash app rename -from NAME [-to NAME] show an application under another name (an existing one merges)
  goliash team rename -from NAME -to NAME   rename a team on all its services
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
  goliash demo                            fill the workspace with three weeks of example data
  goliash version

Every command takes -database (env GOLIASH_DATABASE_URL, default goliash.db) and
-workspace SLUG (env GOLIASH_WORKSPACE, default: the first workspace).
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
