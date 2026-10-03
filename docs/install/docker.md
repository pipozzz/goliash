# Docker and Compose

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

## What the Docker collector reports

- **Compose services**: containers with Compose labels are grouped by project and service. The project is the
  namespace and the replica count is the number of running containers.
- **Other containers** are reported one by one, by container name.
- **Digests**: the registry digest of each running image, when the proxy allows image reads (`IMAGES=1`).
- **Skipped**: containers that belong to Swarm services (use a [Swarm target](swarm.md) for those) and one-off
  `docker compose run` containers.

A `goliash.service` label on a Compose service names its Goliash service; otherwise it waits in the inbox.
Compose service names like `db` or `cache` are deliberately not mapped automatically, because they would merge
unrelated projects.
