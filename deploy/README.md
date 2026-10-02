# Deploying Goliash

| Platform | Server | Agent |
| --- | --- | --- |
| Kubernetes | [`helm/goliash`](helm/goliash) | [`helm/goliash-agent`](helm/goliash-agent) (read-only ClusterRole) |
| Nomad | [`nomad/goliash-server.nomad.hcl`](nomad/goliash-server.nomad.hcl) | [`nomad/goliash-agent.nomad.hcl`](nomad/goliash-agent.nomad.hcl) |
| Docker Swarm | [`swarm/goliash-server.yml`](swarm/goliash-server.yml) | [`swarm/goliash-agent.yml`](swarm/goliash-agent.yml) (with docker-socket-proxy, GET only) |
| ECS | any of the above, or a Fargate task with PostgreSQL | Terraform module [`ecs/goliash-agent`](ecs) (read-only IAM) |
| Docker | [`../docker-compose.yml`](../docker-compose.yml) (`--profile watch-host` watches its own host) | [`docker/goliash-agent.yml`](docker/goliash-agent.yml) (with docker-socket-proxy, GET only) |

Every agent needs a token: create the agent in the UI (**Agents → Add an agent**) or with
`goliash agent create -name …`, then pass the token as `GOLIASH_AGENT_TOKEN` or a file in
`GOLIASH_AGENT_TOKEN_FILE`. Run exactly one agent per token.

## Kubernetes

```sh
helm install goliash deploy/helm/goliash -n goliash --create-namespace \
  --set publicURL=https://goliash.example.com --set ingress.enabled=true \
  --set ingress.hosts[0].host=goliash.example.com
kubectl -n goliash exec deploy/goliash -- goliash login-link -email you@example.com

kubectl -n goliash create secret generic goliash-agent-token --from-literal=token=glsh_agent_…
helm install goliash-agent deploy/helm/goliash-agent -n goliash \
  --set serverURL=https://goliash.example.com --set token.existingSecret=goliash-agent-token
```

The server chart runs one replica with SQLite on a persistent volume, or PostgreSQL via `database.url` /
`database.existingSecret`. SMTP and OIDC settings come from a secret named in `envFromSecret`.

To watch only the cluster Goliash runs in, skip the agent: install the server with `--set collectInCluster=true`
(a read-only ClusterRole for the server) and create a `kubernetes` target without an agent.
