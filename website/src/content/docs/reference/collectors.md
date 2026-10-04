---
title: Collectors
---

Collectors only read. They run in the agent, or in the server for targets without an agent.

| Platform | Reads | Needs | Credentials |
| --- | --- | --- | --- |
| Kubernetes | Deployments, StatefulSets, DaemonSets, CronJobs and their running pods (watch) | ClusterRole with `get`, `list`, `watch` | in-cluster service account, else kubeconfig (`kubeconfig_context`) |
| Amazon ECS | clusters, services, running tasks, task definitions | IAM `ecs:List*`, `ecs:Describe*` | default AWS chain; `credentials_ref` names an AWS profile |
| Nomad | jobs, job versions, allocations | ACL token with `list-jobs` and `read-job` | `credentials_ref` resolves to the token, else `NOMAD_TOKEN` |
| Docker Swarm | services and running tasks | Docker API `GET` (docker-socket-proxy) | none |
| Docker | running containers, grouped into Compose services; image digests | Docker API `GET` on containers and images | none |
| Compose files | services and images declared in Compose files | an HTTP(S) URL, or for an agent a file in `GOLIASH_COMPOSE_DIRS` | `credentials_ref` → bearer token for the URL |

## Target settings

Each target has the settings object of its platform:

```json
{"kubernetes": {"kubeconfig_context": "", "include_namespaces": [], "exclude_namespaces": ["kube-system"]}}
{"ecs": {"region": "eu-west-1", "clusters": ["prod"]}}
{"nomad": {"address": "https://nomad.service.consul:4646", "region": "", "namespaces": []}}
{"swarm": {"docker_host": "tcp://socket-proxy:2375"}}
{"docker": {"docker_host": "tcp://socket-proxy:2375", "projects": ["shop"]}}
{"compose": {"files": ["https://raw.githubusercontent.com/acme/infra/main/compose.yaml"], "project": "shop", "variables": {"TAG": "1.3.0"}}}
```

Empty lists mean everything. `docker_host` takes `tcp://`, `https://` or `unix:///var/run/docker.sock`. The poll
interval is at least 30 seconds and defaults to 300; Kubernetes also sends a snapshot shortly after a change.

## Credentials

The server never sends secrets, only a `credentials_ref` name. The agent resolves it from:

1. the environment variable `GOLIASH_CREDENTIAL_<NAME>`, with the name upper-cased and anything that is not a
   letter or digit replaced by `_`, or
2. the file `$GOLIASH_CREDENTIALS_DIR/<name>` (default `/etc/goliash-agent/credentials`).

## Private registries

Images from registries other than the public ones are checked by the agents. For each repository the credential is
looked up under the registry host's name, for example `GOLIASH_CREDENTIAL_REGISTRY_EXAMPLE_COM`:

- **Distribution API registries** (Harbor, GitLab, Artifactory, Nexus, a private Docker Hub repository, …):
  `user:password`, or a token. Without a credential the agent tries anonymously.
- **Amazon ECR** (`<account>.dkr.ecr.<region>.amazonaws.com`): the agent uses the ECR API with its AWS credentials
  and needs IAM `ecr:ListImages`. The default chain is used: IRSA, an ECS task role, an instance profile or the
  environment. A credential, if set, names an AWS profile.
