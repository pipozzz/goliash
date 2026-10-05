#!/usr/bin/env bash
# Agent compatibility: an agent from an older release reports to this build's
# server, which must accept it and show what it collects.
#
#   scripts/agent-compat-test.sh 0.9.0
set -euo pipefail

from=${1:?usage: agent-compat-test.sh AGENT_VERSION}
work=$(mktemp -d)
trap 'kill "${server:-0}" "${agent:-0}" 2>/dev/null || true; rm -rf "$work"' EXIT

os=$(go env GOOS)
arch=$(go env GOARCH)
echo "== agent v$from against this server on $os/$arch"
curl -fsSL "https://github.com/pipozzz/goliash/releases/download/v$from/goliash-agent_${from}_${os}_${arch}.tar.gz" |
  tar xz -C "$work" goliash-agent
go build -o "$work/goliash" ./cmd/goliash

port=$((20000 + RANDOM % 20000))
export GOLIASH_DATABASE_URL=$work/goliash.db GOLIASH_PUBLIC_URL=http://127.0.0.1:$port
mkdir "$work/stack"
cat > "$work/stack/compose.yml" <<'YAML'
services:
  web:
    image: nginx:1.27.2
    labels:
      goliash.service: web
YAML
"$work/goliash" env create -name prod >/dev/null
token=$("$work/goliash" agent create -name edge | tail -1)
"$work/goliash" target create -agent edge -env prod -platform compose -name stack \
  -settings "{\"compose\":{\"files\":[\"$work/stack/compose.yml\"]}}" -poll 30 >/dev/null

GOLIASH_LISTEN=127.0.0.1:$port "$work/goliash" serve > "$work/server.log" 2>&1 &
server=$!
for _ in $(seq 50); do curl -fsS "http://127.0.0.1:$port/readyz" >/dev/null 2>&1 && break; sleep 0.2; done

GOLIASH_SERVER_URL=http://127.0.0.1:$port GOLIASH_AGENT_TOKEN=$token GOLIASH_COMPOSE_DIRS=$work/stack \
  "$work/goliash-agent" -data-dir "$work/agent" > "$work/agent.log" 2>&1 &
agent=$!

for _ in $(seq 60); do
  if "$work/goliash" matrix 2>/dev/null | grep -q "^web .*1\.27\.2"; then
    "$work/goliash" agent list | grep -q "edge .*$from" || { echo "agent version not recorded"; "$work/goliash" agent list; exit 1; }
    echo "ok: agent v$from reported nginx 1.27.2 as web"
    exit 0
  fi
  sleep 1
done
echo "the server did not show what agent v$from collected"
echo "--- agent"; tail -20 "$work/agent.log"; echo "--- server"; tail -20 "$work/server.log"
exit 1
