---
title: Kubernetes
description: 'Install Goliash and its agent on Kubernetes with signed Helm charts and a read-only ClusterRole.'
---

The charts are published with every release as signed OCI artifacts in GHCR:
`oci://ghcr.io/pipozzz/charts/goliash` and `oci://ghcr.io/pipozzz/charts/goliash-agent`. The chart version equals the
Goliash version. The sources are in the repository under `deploy/helm`.

## Server

```sh
helm install goliash oci://ghcr.io/pipozzz/charts/goliash -n goliash --create-namespace \
  --set publicURL=https://goliash.example.com \
  --set ingress.enabled=true --set ingress.hosts[0].host=goliash.example.com
kubectl -n goliash exec deploy/goliash -- goliash login-link -email you@example.com
```

The chart runs one replica with SQLite on a persistent volume. For PostgreSQL, set `database.url` or
`database.existingSecret`.

SMTP, OIDC and the secret key come from a secret named in `envFromSecret`:

```sh
kubectl -n goliash create secret generic goliash-env \
  --from-literal=GOLIASH_SECRET_KEY="$(openssl rand -base64 32)" \
  --from-literal=GOLIASH_SMTP_ADDR=smtp.example.com:587 \
  --from-literal=GOLIASH_SMTP_FROM=goliash@example.com
helm upgrade goliash oci://ghcr.io/pipozzz/charts/goliash -n goliash --reuse-values --set envFromSecret=goliash-env
```

With SQLite the secret key is otherwise created next to the database on the volume. With PostgreSQL, set it as
shown.

### Watch the cluster the server runs in

To watch only the cluster Goliash runs in, skip the agent. Install the server with `--set collectInCluster=true`,
which gives the server a read-only ClusterRole, and create a `kubernetes` target without an agent.

## Agent

Create the agent in the UI (**Agents → Add an agent**) or with `goliash agent create -name prod-eu`, and keep the
token in a secret:

```sh
kubectl -n goliash create secret generic goliash-agent-token --from-literal=token=glsh_agent_…
helm install goliash-agent oci://ghcr.io/pipozzz/charts/goliash-agent -n goliash \
  --set serverURL=https://goliash.example.com --set token.existingSecret=goliash-agent-token
```

Then add a `kubernetes` target for the agent, for example with
`{"kubernetes":{"exclude_namespaces":["kube-system"]}}`.

The agent's ClusterRole allows only `get`, `list` and `watch` on Deployments, ReplicaSets, StatefulSets,
DaemonSets, CronJobs, Jobs and Pods. It watches for changes and sends a snapshot shortly after a rollout, and a full
snapshot on the poll interval.

### Private registries

The agent checks the tags of private registries with credentials from its own environment, never from the server.
Put them in a secret and name it in `credentialsSecret`; every key becomes a file in
`/etc/goliash-agent/credentials`. The key is the registry host, for example `registry.example.com` with
`user:password` or a token.

For Amazon ECR on EKS, give the agent an IAM role with `ecr:ListImages` through IRSA:

```sh
helm upgrade goliash-agent oci://ghcr.io/pipozzz/charts/goliash-agent -n goliash --reuse-values \
  --set-string 'serviceAccount.annotations.eks\.amazonaws\.com/role-arn=arn:aws:iam::123456789012:role/goliash-agent'
```

## Verify the charts

```sh
cosign verify ghcr.io/pipozzz/charts/goliash:0.2.0 \
  --certificate-identity-regexp '^https://github.com/pipozzz/goliash/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```
