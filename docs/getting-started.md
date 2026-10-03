# Getting started

This starts a server with Docker Compose, shows example data, and connects your first orchestrator. It takes about
five minutes.

## 1. Start the server

```sh
git clone https://github.com/pipozzz/goliash.git && cd goliash
docker compose up -d --build
```

The server listens on <http://localhost:8080> and keeps its SQLite database in a Docker volume.

## 2. Sign in

```sh
docker compose exec goliash goliash login-link -email you@example.com
```

Open the printed link. The first person to sign in becomes the owner of the organization. When SMTP is configured,
people can also request links by e-mail on the sign-in page; see [Configuration](reference/configuration.md).

## 3. Look around with example data (optional)

```sh
docker compose exec goliash goliash demo
```

This fills the workspace with three weeks of realistic history: four targets, seven services, rollouts, drift and
new upstream releases. The demo targets are named `demo-*`, so they are easy to tell apart from real ones.

## 4. Connect what you run

There are two ways to collect a target.

=== "With an agent"

    An agent runs next to the orchestrator and sends snapshots to the server over HTTPS. Use it when the server
    cannot reach the orchestrator, or when credentials must stay in that network.

    1. In **Agents**, add the environments you deploy to (for example dev, staging, prod), then **Add an agent**
       and copy its token. The token is shown once.
    2. Add a target for the agent: its platform, environment and settings.
    3. Run the agent where it can reach the orchestrator:

    ```sh
    docker run -d --name goliash-agent -v goliash-agent:/data \
      -e GOLIASH_SERVER_URL=https://goliash.example.com \
      -e GOLIASH_AGENT_TOKEN=glsh_agent_… \
      ghcr.io/pipozzz/goliash-agent:latest
    ```

    Ready-made manifests for each platform are under [Install](install/index.md).

=== "Without an agent"

    The server can collect targets itself, with the same read-only collectors. Create a target and pick **the
    server itself** instead of an agent. For example, to watch the Docker host the quickstart runs on:

    ```sh
    docker compose --profile watch-host up -d --build      # adds a read-only docker-socket-proxy
    docker compose exec goliash goliash env create -name prod -position 30
    docker compose exec goliash goliash target create -env prod -platform docker -name this-host \
      -settings '{"docker":{"docker_host":"tcp://socket-proxy:2375"}}'
    ```

Within a minute the first snapshot arrives and workloads appear.

## 5. Map workloads to services

Workloads labelled `goliash.service` or `app.kubernetes.io/name` map to services by themselves. Everything else
waits in the **Inbox**, grouped by image with a suggested service name. One click maps every workload that runs the
image and adds a rule, so the next ones map by themselves. See [Matrix and mapping](guide/matrix.md).

## Next steps

- Tune what counts as "behind": [Versions and drift](guide/versions.md).
- Get told about new releases and drift: [Notifications](guide/notifications.md).
- Invite your team or set up a workspace per client: [Teams and workspaces](guide/teams.md).
- Run it for real: [Install](install/index.md).
