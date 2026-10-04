#!/usr/bin/env bash
# Copyright 2026 The Goliash Authors
# SPDX-License-Identifier: AGPL-3.0-only
#
# End-to-end test on a real Kubernetes cluster (kind): builds both images, installs the
# server and the agent with the Helm charts in deploy/helm, runs a workload and checks
# that it shows up in the matrix and that a rollout becomes a version_changed event.
#
#   kind create cluster --name goliash-e2e
#   scripts/e2e/kubernetes.sh
#
# Needs docker, kind, kubectl and helm. Set KIND_CLUSTER for another cluster name.
set -euo pipefail

cluster=${KIND_CLUSTER:-goliash-e2e}
ns=goliash
cd "$(dirname "$0")/../.."

say() { printf '\n== %s\n' "$*"; }

dump() {
  say "Diagnostics"
  kubectl -n "$ns" get all || true
  kubectl -n "$ns" logs deploy/goliash --tail=80 || true
  kubectl -n "$ns" logs deploy/goliash-agent --tail=80 || true
  kubectl -n e2e get deploy,pods -o wide || true
}
trap 'dump' ERR

# wait_for DESCRIPTION SECONDS COMMAND... runs COMMAND until it succeeds.
wait_for() {
  local what=$1 seconds=$2; shift 2
  for _ in $(seq 1 "$seconds"); do
    if "$@" >/dev/null 2>&1; then return 0; fi
    sleep 1
  done
  echo "timed out after ${seconds}s waiting for: $what" >&2
  return 1
}

goliash() { kubectl -n "$ns" exec deploy/goliash -- goliash "$@"; }

say "Building images"
docker build -q --target server -t goliash:e2e .
docker build -q --target agent -t goliash-agent:e2e .
kind load docker-image goliash:e2e goliash-agent:e2e --name "$cluster"

say "Installing the server chart"
kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f -
helm upgrade --install goliash deploy/helm/goliash -n "$ns" --wait --timeout 3m \
  --set image.repository=goliash --set image.tag=e2e --set image.pullPolicy=Never \
  --set publicURL=http://goliash.goliash.svc

say "Creating an environment, an agent and a target"
goliash env create -name prod -position 30
token=$(goliash agent create -name kind | tail -1)
[[ $token == glsh_agent_* ]] || { echo "no agent token: $token" >&2; exit 1; }
goliash target create -agent kind -env prod -platform kubernetes -name kind -poll 30 \
  -settings '{"kubernetes":{"include_namespaces":["e2e"]}}'

say "Installing the agent chart (read-only ClusterRole)"
kubectl -n "$ns" create secret generic goliash-agent-token --from-literal=token="$token" \
  --dry-run=client -o yaml | kubectl apply -f -
helm upgrade --install goliash-agent deploy/helm/goliash-agent -n "$ns" --wait --timeout 3m \
  --set image.repository=goliash-agent --set image.tag=e2e --set image.pullPolicy=Never \
  --set serverURL=http://goliash.goliash.svc --set token.existingSecret=goliash-agent-token

say "The agent may only read"
sa=system:serviceaccount:$ns:goliash-agent
kubectl auth can-i list pods --all-namespaces --as "$sa" | grep -qx yes
for verb in create delete patch update; do
  if [[ $(kubectl auth can-i "$verb" deployments --all-namespaces --as "$sa") != no ]]; then
    echo "the agent may $verb deployments" >&2; exit 1
  fi
done

say "Running a workload"
kubectl create namespace e2e --dry-run=client -o yaml | kubectl apply -f -
kubectl -n e2e apply -f - <<'YAML'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  labels: { app.kubernetes.io/name: web }
spec:
  replicas: 2
  selector: { matchLabels: { app.kubernetes.io/name: web } }
  template:
    metadata:
      labels: { app.kubernetes.io/name: web }
    spec:
      containers:
        - name: nginx
          image: nginx:1.27.2-alpine
YAML
kubectl -n e2e rollout status deploy/web --timeout=3m

say "The matrix shows web 1.27.2-alpine in prod"
wait_for "web in the matrix" 120 bash -c "kubectl -n $ns exec deploy/goliash -- goliash matrix | grep -E '^web .*1\.27\.2-alpine \(2\)'"
goliash matrix

say "A rollout becomes a version_changed event"
kubectl -n e2e set image deploy/web nginx=nginx:1.27.3-alpine
kubectl -n e2e rollout status deploy/web --timeout=3m
wait_for "version_changed event" 120 bash -c "kubectl -n $ns exec deploy/goliash -- goliash events -service web | grep -E 'version_changed.*1\.27\.2-alpine.*1\.27\.3-alpine'"
goliash events -service web

say "The API answers"
kubectl -n "$ns" exec deploy/goliash -- goliash healthcheck

trap - ERR
say "Kubernetes end-to-end test passed"
