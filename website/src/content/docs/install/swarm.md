---
title: Docker Swarm
description: 'Deploy Goliash on Docker Swarm with a stack file and watch services through a read-only socket proxy.'
---

## Server

`deploy/swarm/goliash-server.yml` runs the server with SQLite on a local volume, so it is pinned to one node:

```sh
docker stack deploy -c deploy/swarm/goliash-server.yml goliash
docker service logs goliash_server 2>&1 | grep link=      # the setup link for the first account
```

Edit `GOLIASH_PUBLIC_URL` in the file first. For PostgreSQL, set `GOLIASH_DATABASE_URL` and drop the placement
constraint.

## Agent

`deploy/swarm/goliash-agent.yml` runs the agent with a docker-socket-proxy that allows only `GET` on services,
tasks and nodes. The proxy runs on a manager node, because the services and tasks APIs answer only there.

```sh
printf '%s' 'glsh_agent_…' | docker secret create goliash_agent_token -
GOLIASH_SERVER_URL=https://goliash.example.com docker stack deploy -c deploy/swarm/goliash-agent.yml goliash-agent
```

Create a `swarm` target for the agent with `{"swarm":{"docker_host":"tcp://socket-proxy:2375"}}`.

When the server runs in the same swarm, uncomment the `goliash` network in the agent file and use
`GOLIASH_SERVER_URL=http://goliash_server:8080`.

## What the Swarm collector reports

Services with their desired and running replicas, and the image of every running task, digest included. During a
rolling update both versions show. The stack name (`com.docker.stack.namespace`) is the namespace.
