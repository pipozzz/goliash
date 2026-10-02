#!/usr/bin/env bash
# Copyright 2026 The Goliash Authors
# SPDX-License-Identifier: AGPL-3.0-only
#
# Stops the lab started by up.sh. Keeps .lab/ (database, agent token), so the next
# up.sh continues where it stopped; delete .lab/ to start over.
#
#   scripts/lab/down.sh                # stop Goliash and the k3s, Swarm and Nomad containers
#   scripts/lab/down.sh --goliash-only # stop the server and agent only
set -uo pipefail

LAB=$(cd "$(dirname "$0")/../.." && pwd)/.lab
for p in agent server; do
  if [ -f "$LAB/$p.pid" ]; then
    kill "$(cat "$LAB/$p.pid")" 2>/dev/null || true
    rm -f "$LAB/$p.pid"
  fi
done
[ "${1:-}" = "--goliash-only" ] && exit 0

for c in goliash-k3s goliash-dind goliash-nomad; do
  docker stop "$c" >/dev/null 2>&1 && echo "stopped $c"
done
echo "Lab stopped. scripts/lab/up.sh starts it again."
