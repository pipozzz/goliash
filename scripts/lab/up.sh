#!/usr/bin/env bash
# Copyright 2026 The Goliash Authors
# SPDX-License-Identifier: AGPL-3.0-only
#
# Starts a local lab: Kubernetes (k3s), Docker Swarm and a plain Docker host (one dind) and Nomad in Docker, a
# Goliash server on http://127.0.0.1:18090 and an agent that collects all of them.
#
#   scripts/lab/up.sh      # prints a sign-in link
#   scripts/lab/down.sh
#
# Needs Docker and Go. State lives in .lab/ (gitignored).
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
LAB=$ROOT/.lab
URL=http://127.0.0.1:18090
mkdir -p "$LAB"
cd "$ROOT"

say() { printf '\033[1m==> %s\033[0m\n' "$*"; }
running() { [ "$(docker inspect -f '{{.State.Running}}' "$1" 2>/dev/null)" = "true" ]; }

say "Building goliash and goliash-agent"
go build -o bin/goliash ./cmd/goliash
go build -o bin/goliash-agent ./cmd/goliash-agent

# --- Kubernetes ------------------------------------------------------------------
if ! running goliash-k3s; then
  say "Starting k3s"
  docker rm -f goliash-k3s >/dev/null 2>&1 || true
  docker run -d --privileged --name goliash-k3s -p 16443:6443 -e K3S_KUBECONFIG_MODE=644 \
    rancher/k3s:v1.33.4-k3s1 server --disable traefik --disable metrics-server --tls-san 127.0.0.1 >/dev/null
fi
until docker exec goliash-k3s kubectl get nodes 2>/dev/null | grep -q ' Ready'; do sleep 2; done
docker exec -i goliash-k3s kubectl apply -f - >/dev/null <<'YAML'
apiVersion: v1
kind: Namespace
metadata: {name: goliash}
---
apiVersion: v1
kind: ServiceAccount
metadata: {name: goliash-agent, namespace: goliash}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata: {name: goliash-agent-readonly}
rules:
  - {apiGroups: ["apps"], resources: ["deployments", "replicasets", "statefulsets", "daemonsets"], verbs: ["get", "list", "watch"]}
  - {apiGroups: ["batch"], resources: ["cronjobs", "jobs"], verbs: ["get", "list", "watch"]}
  - {apiGroups: [""], resources: ["pods"], verbs: ["get", "list", "watch"]}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata: {name: goliash-agent-readonly}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: goliash-agent-readonly}
subjects: [{kind: ServiceAccount, name: goliash-agent, namespace: goliash}]
---
apiVersion: v1
kind: Namespace
metadata: {name: shop}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: web, namespace: shop, labels: {app.kubernetes.io/name: web}}
spec:
  replicas: 2
  selector: {matchLabels: {app: web}}
  template:
    metadata: {labels: {app: web}}
    spec:
      containers: [{name: nginx, image: "nginx:1.27.2"}]
---
apiVersion: batch/v1
kind: CronJob
metadata: {name: report, namespace: shop}
spec:
  schedule: "0 3 * * *"
  jobTemplate:
    spec:
      template:
        spec:
          restartPolicy: Never
          containers: [{name: report, image: "busybox:1.36.1", command: ["true"]}]
YAML
# A kubeconfig for the agent with the read-only service account only.
TOKEN=$(docker exec goliash-k3s kubectl -n goliash create token goliash-agent --duration 24h)
CA=$(docker exec goliash-k3s cat /etc/rancher/k3s/k3s.yaml | awk '/certificate-authority-data/ {print $2}')
cat > "$LAB/kubeconfig" <<KC
apiVersion: v1
kind: Config
clusters: [{name: k3s, cluster: {server: "https://127.0.0.1:16443", certificate-authority-data: $CA}}]
users: [{name: goliash-agent, user: {token: "$TOKEN"}}]
contexts: [{name: goliash-agent@k3s, context: {cluster: k3s, user: goliash-agent}}]
current-context: goliash-agent@k3s
KC

# --- Docker Swarm ----------------------------------------------------------------
if ! running goliash-dind; then
  say "Starting Docker Swarm (docker-in-docker)"
  docker rm -f goliash-dind >/dev/null 2>&1 || true
  docker run -d --privileged --name goliash-dind -e DOCKER_TLS_CERTDIR= -p 12375:2375 docker:27-dind >/dev/null
fi
until docker exec goliash-dind docker info >/dev/null 2>&1; do sleep 2; done
docker exec goliash-dind docker swarm init >/dev/null 2>&1 || true
docker exec goliash-dind docker service inspect shop_web >/dev/null 2>&1 ||
  docker exec goliash-dind docker service create -q --name shop_web --label com.docker.stack.namespace=shop \
    --label goliash.service=web --replicas 2 nginx:1.27.3 >/dev/null
# A plain container with Compose labels on the same engine, for the docker platform
# (Swarm tasks above are left to the swarm target).
docker exec goliash-dind docker inspect shop-web-1 >/dev/null 2>&1 ||
  docker exec goliash-dind docker run -d -q --name shop-web-1 --label com.docker.compose.project=shop \
    --label com.docker.compose.service=web --label goliash.service=web nginx:1.27.2 >/dev/null

# --- Nomad -----------------------------------------------------------------------
# Server only: a Nomad client cannot run in Docker on macOS, so jobs register but do not run.
if ! running goliash-nomad; then
  say "Starting Nomad"
  docker rm -f goliash-nomad >/dev/null 2>&1 || true
  docker run -d --name goliash-nomad -p 14646:4646 hashicorp/nomad:1.9 \
    agent -server -bootstrap-expect 1 -data-dir /tmp/nomad -bind 0.0.0.0 >/dev/null
fi
until curl -sf http://127.0.0.1:14646/v1/status/leader | grep -q ':'; do sleep 2; done
curl -sf -X PUT http://127.0.0.1:14646/v1/jobs -d '{"Job":{"ID":"payments","Name":"payments","Type":"service",
  "Datacenters":["*"],"Meta":{"goliash.service":"payments-api"},"TaskGroups":[{"Name":"web","Count":2,
  "Tasks":[{"Name":"app","Driver":"docker","Config":{"image":"ghcr.io/acme/payments-api:1.5.0"},
  "Resources":{"CPU":50,"MemoryMB":32}}]}]}}' >/dev/null

# --- Goliash ---------------------------------------------------------------------
"$ROOT/scripts/lab/down.sh" --goliash-only
export GOLIASH_DATABASE_URL=$LAB/goliash.db GOLIASH_PUBLIC_URL=$URL
G=$ROOT/bin/goliash
if [ ! -f "$LAB/goliash.db" ]; then
  say "Configuring Goliash"
  $G env create -name dev -position 10 >/dev/null
  $G env create -name staging -position 20 >/dev/null
  $G env create -name prod -position 30 >/dev/null
  $G agent create -name lab | tail -1 > "$LAB/agent-token"
  $G target create -agent lab -env prod -platform kubernetes -name k3s-prod -poll 60 \
    -settings '{"kubernetes":{"exclude_namespaces":["kube-system","goliash"]}}' >/dev/null
  $G target create -agent lab -env staging -platform swarm -name swarm-staging -poll 30 \
    -settings '{"swarm":{"docker_host":"tcp://127.0.0.1:12375"}}' >/dev/null
  $G target create -agent lab -env dev -platform docker -name docker-dev -poll 30 \
    -settings '{"docker":{"docker_host":"tcp://127.0.0.1:12375"}}' >/dev/null
  $G target create -agent lab -env dev -platform nomad -name nomad-dev -poll 60 \
    -settings '{"nomad":{"address":"http://127.0.0.1:14646"}}' >/dev/null
  $G service set -name web -owner team-web -track minor >/dev/null
fi

say "Starting server and agent"
$G -listen 127.0.0.1:18090 >> "$LAB/server.log" 2>&1 &
echo $! > "$LAB/server.pid"
until curl -sf "$URL/healthz" >/dev/null; do sleep 0.5; done
KUBECONFIG=$LAB/kubeconfig GOLIASH_SERVER_URL=$URL GOLIASH_AGENT_TOKEN=$(cat "$LAB/agent-token") \
  "$ROOT/bin/goliash-agent" -data-dir "$LAB/agent-data" >> "$LAB/agent.log" 2>&1 &
echo $! > "$LAB/agent.pid"

LINK=$($G login-link -email lab@example.com | tail -1)
cat <<MSG

Goliash lab is up: $URL  (logs in .lab/server.log and .lab/agent.log)
Sign in (works once, 15 minutes):
  $LINK

Try a rollout and watch the matrix update:
  docker exec goliash-k3s kubectl -n shop set image deployment/web nginx=nginx:1.27.3
MSG
