---
title: Collectors
description: 'What each collector reads from Kubernetes, ECS, Lambda, Nomad, Swarm, Docker and Compose, and the permissions it needs.'
---

Collectors only read. They run in the agent, or in the server for targets without an agent.

| Platform | Reads | Needs | Credentials |
| --- | --- | --- | --- |
| Kubernetes | Deployments, StatefulSets, DaemonSets, CronJobs and their running pods (watch) | ClusterRole with `get`, `list`, `watch` | in-cluster service account, else kubeconfig (`kubeconfig_context`) |
| Amazon ECS | clusters, services, running tasks, task definitions | IAM `ecs:List*`, `ecs:Describe*` | default AWS chain; `credentials_ref` names an AWS profile |
| AWS Lambda | functions of a region and their aliases: container image and digest, or the runtime of .zip functions; tags | IAM `lambda:ListFunctions`, `lambda:GetFunction`, `lambda:ListAliases` | default AWS chain; `credentials_ref` names an AWS profile |
| Nomad | jobs, job versions, allocations | ACL token with `list-jobs` and `read-job` | `credentials_ref` resolves to the token, else `NOMAD_TOKEN` |
| Docker Swarm | services and running tasks | Docker API `GET` (docker-socket-proxy) | none |
| Docker | running containers, grouped into Compose services; image digests | Docker API `GET` on containers and images | none |
| Compose files | services and images declared in Compose files | an HTTP(S) URL, or for an agent a file in `GOLIASH_COMPOSE_DIRS` | `credentials_ref` → bearer token for the URL |
| Manual | versions people enter on a service page (**Add by hand**), for software Goliash does not collect | nothing: never collected, never stale | none |

## Target settings

Each target has the settings object of its platform:

```json
{"kubernetes": {"kubeconfig_context": "", "include_namespaces": [], "exclude_namespaces": ["kube-system"]}}
{"ecs": {"region": "eu-west-1", "clusters": ["prod"]}}
{"nomad": {"address": "https://nomad.service.consul:4646", "region": "", "namespaces": []}}
{"swarm": {"docker_host": "tcp://socket-proxy:2375"}}
{"docker": {"docker_host": "tcp://socket-proxy:2375", "projects": ["shop"]}}
{"lambda": {"region": "eu-west-1", "name_prefixes": ["shop-"], "alias_environments": {"live": "prod", "canary": "-"}}}
{"compose": {"files": ["https://raw.githubusercontent.com/acme/infra/main/compose.yaml"], "project": "shop", "variables": {"TAG": "1.3.0"}}}
```

Nomad: each job is a workload. A periodic or parameterized job is one workload (a cron job) that counts its
running runs, rather than a new workload for every `backup/periodic-…` or `export/dispatch-…` run.

Lambda: each function is a workload, its tags are labels (so `goliash.app` and `goliash.service` work as tags). A
function built from a container image reports that image and digest, checked for new tags like any image. A .zip
function reports its runtime as the matching AWS base image, `python3.12` as `public.ecr.aws/lambda/python:3.12`
and `nodejs20.x` as `public.ecr.aws/lambda/nodejs:20`: Goliash then tells when a newer runtime exists and, from
endoflife.date, when AWS deprecates the one in use. The inbox suggests the function's name as its service, so
functions on the same runtime stay apart.

A function with **aliases** shows once per alias, with the version the alias points to, in the environment named
like the alias: `dev`, `staging` and `prod` aliases fill three columns of the matrix, so a version waiting to be
promoted shows as drift like anywhere else. `alias_environments` maps aliases named otherwise (`live` to `prod`),
and `-` leaves one out (a `canary`); aliases that match no environment stay in the target's. A function without
aliases shows its latest code (`$LATEST`). Without `lambda:ListAliases` the agent reports latest code only and says
so in the target's status.

Empty lists mean everything. `docker_host` takes `tcp://`, `https://` or `unix:///var/run/docker.sock`. The poll
interval is at least 30 seconds and defaults to 300; Kubernetes also sends a snapshot shortly after a change.

## Credentials

The server never sends secrets, only a `credentials_ref` name. The agent resolves it from:

1. the environment variable `GOLIASH_CREDENTIAL_<NAME>`, with the name upper-cased and anything that is not a
   letter or digit replaced by `_`, or
2. the file `$GOLIASH_CREDENTIALS_DIR/<name>` (default `/etc/goliash-agent/credentials`).

## Private registries

Images from registries other than the public ones are checked by the agents. So are private repositories on public
registries (GHCR, Docker Hub, Quay, …): when a registry refuses the server's anonymous check, the repository is
handed to the agents. **Check upstream now** on the service page tries anonymously again, in case it became public.

For each repository the agent looks for credentials for the registry host, in this order:

1. the credential named after the host, for example `GOLIASH_CREDENTIAL_REGISTRY_EXAMPLE_COM` or the file
   `registry.example.com` in the credentials directory;
2. its Docker config: `$DOCKER_CONFIG/config.json`, else `~/.docker/config.json`, as `docker login` writes it.
   Entries kept by a credential helper (`credsStore`, `credHelpers`, as Docker Desktop does) hold no secret and are
   skipped;
3. on Kubernetes, with `GOLIASH_READ_PULL_SECRETS=true` (Helm: `rbac.readPullSecrets`), the image pull secrets that
   running pods of its targets reference. It needs `get` on secrets;
4. for **Google Artifact Registry** (`*-docker.pkg.dev`) and **Container Registry** (`gcr.io`), the service account
   the agent runs as, from the metadata server: GKE Workload Identity or a Compute Engine service account with
   `roles/artifactregistry.reader`;
5. for **Azure Container Registry** (`*.azurecr.io`), AKS Workload Identity: the federated token the pod gets
   (`AZURE_FEDERATED_TOKEN_FILE`, `AZURE_CLIENT_ID`, `AZURE_TENANT_ID`) is exchanged for an ACR token. The identity
   needs the `AcrPull` role;
6. anonymous access.

When a host has several credentials (two pull secrets for different Harbor projects), each is tried in turn.
Credentials never leave the agent.

- **Distribution API registries** (Harbor, GitLab, Artifactory, Nexus, GHCR, Docker Hub, …): `user:password`, or a
  token.
- **Amazon ECR** (`<account>.dkr.ecr.<region>.amazonaws.com`): the agent uses the ECR API with its AWS credentials
  and needs IAM `ecr:ListImages`. The default chain is used: IRSA, an ECS task role, an instance profile or the
  environment. A credential, if set, names an AWS profile.
