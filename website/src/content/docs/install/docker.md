---
title: Docker and Compose
---

## Server

The repository's `docker-compose.yml` runs the server with SQLite on a volume:

```sh
git clone https://github.com/pipozzz/goliash.git && cd goliash
docker compose up -d --build
docker compose exec goliash goliash login-link -email you@example.com
```

To use the published image instead of building it, replace `build:` with
`image: ghcr.io/pipozzz/goliash:latest`. Set `GOLIASH_PUBLIC_URL` to the address people use, and put a reverse
proxy with TLS in front for anything beyond a trial.

Without Compose:

```sh
docker run -d --name goliash -p 8080:8080 -v goliash-data:/data \
  -e GOLIASH_PUBLIC_URL=https://goliash.example.com \
  ghcr.io/pipozzz/goliash:latest
```

The image's health check command is `goliash healthcheck`.

## Watch the host the server runs on

No agent is needed. The `watch-host` profile adds a
[docker-socket-proxy](https://github.com/Tecnativa/docker-socket-proxy) that allows only `GET` on containers and
images:

```sh
docker compose --profile watch-host up -d --build
docker compose exec goliash goliash env create -name prod -position 30
docker compose exec goliash goliash target create -env prod -platform docker -name this-host \
  -settings '{"docker":{"docker_host":"tcp://socket-proxy:2375"}}'
```

## Agent on another host

`deploy/docker/goliash-agent.yml` runs an agent with its own socket proxy on any Docker host:

```sh
export GOLIASH_SERVER_URL=https://goliash.example.com
export GOLIASH_AGENT_TOKEN=glsh_agent_…
docker compose -f deploy/docker/goliash-agent.yml up -d
```

Create a `docker` target for the agent with `{"docker":{"docker_host":"tcp://socket-proxy:2375"}}`.

## Track only some Compose projects

To watch one stack instead of the whole host, list its Compose projects:

```json
{"docker": {"docker_host": "tcp://socket-proxy:2375", "projects": ["shop"]}}
```

## Track a Compose file without access to Docker

A `compose` target reads Compose files as declared, without a Docker engine, socket proxy or agent. It suits stacks
kept in Git, or deployed through a platform such as Dokploy, Nomploy or Coolify where Goliash cannot reach the host:

```sh
goliash target create -env prod -platform compose -name shop \
  -settings '{"compose":{"files":["https://raw.githubusercontent.com/acme/infra/main/shop/compose.yaml"]}}'
```

- **Several files** override each other in order, like `docker compose -f compose.yaml -f compose.prod.yaml`.
- **Variables:** `${TAG:-1.2.3}` uses its default; set values with `"variables": {"TAG": "1.3.0"}`.
- **Private repositories:** give the target a credentials reference; the URL is fetched with
  `Authorization: Bearer <credential>` (a GitHub or GitLab token).
- **What it reports:** every service with an `image`, with its declared replicas. Services built from source without
  an `image` are skipped. The project is `"project"` from the settings, else the file's `name`.
- **Files on disk:** an agent can read local files from directories its operator lists in `GOLIASH_COMPOSE_DIRS`, for
  example `/srv/stacks`. The server itself reads URLs only, and never link-local addresses such as cloud metadata.

Declared versions are what the file says, not what runs. Add a `docker` target for the same environment as well,
and Goliash compares the two: `declared` drift shows where the host runs something other than what Git declares.
See [Versions and drift](/guide/versions/#declared-versus-running).

## What the Docker collector reports

- **Compose services**: containers with Compose labels are grouped by project and service. The project is the
  namespace and the replica count is the number of running containers.
- **Other containers** are reported one by one, by container name.
- **Digests**: the registry digest of each running image, when the proxy allows image reads (`IMAGES=1`).
- **Skipped**: containers that belong to Swarm services (use a [Swarm target](/install/swarm/) for those) and one-off
  `docker compose run` containers.

A `goliash.service` label on a Compose service names its Goliash service; otherwise it waits in the inbox.
Compose service names like `db` or `cache` are deliberately not mapped automatically, because they would merge
unrelated projects.
