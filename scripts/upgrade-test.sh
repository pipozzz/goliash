#!/usr/bin/env bash
# Upgrade test: an older release fills a database, this build migrates it and must
# show the same services and serve. Run for SQLite (default) or PostgreSQL:
#
#   scripts/upgrade-test.sh 0.1.0
#   scripts/upgrade-test.sh 0.8.0 postgres://user:pass@localhost:5432/empty_db?sslmode=disable
#
# The PostgreSQL database must be empty.
set -euo pipefail

from=${1:?usage: upgrade-test.sh FROM_VERSION [POSTGRES_DSN]}
dsn=${2:-}
work=$(mktemp -d)
trap 'kill "${pid:-0}" 2>/dev/null || true; rm -rf "$work"' EXIT

os=$(go env GOOS)
arch=$(go env GOARCH)
dialect=sqlite
[[ -n $dsn ]] && dialect=postgres
echo "== upgrade from v$from ($dialect) on $os/$arch"
curl -fsSL "https://github.com/pipozzz/goliash/releases/download/v$from/goliash_${from}_${os}_${arch}.tar.gz" | tar xz -C "$work" goliash
mv "$work/goliash" "$work/old"
go build -o "$work/new" ./cmd/goliash

export GOLIASH_DATABASE_URL=${dsn:-$work/goliash.db}
export GOLIASH_SECRET_KEY=$(head -c 32 /dev/urandom | base64)

"$work/old" demo >/dev/null
"$work/old" channel create -type webhook -name ops -url https://example.com/hook -secret s3cr3t >/dev/null
"$work/old" matrix > "$work/before.txt"
services() { awk 'NR > 1 && NF { print $1 }' "$1" | sort -u; }

"$work/new" matrix > "$work/after.txt"
if ! diff <(services "$work/before.txt") <(services "$work/after.txt"); then
  echo "services differ after the upgrade"
  exit 1
fi
[[ $(services "$work/after.txt" | wc -l) -ge 5 ]] || { echo "too few services: $(cat "$work/after.txt")"; exit 1; }
"$work/new" events -limit 5 > /dev/null
"$work/new" drift > /dev/null

port=$((20000 + RANDOM % 20000))
GOLIASH_LISTEN=127.0.0.1:$port GOLIASH_PUBLIC_URL=http://127.0.0.1:$port "$work/new" serve > "$work/serve.log" 2>&1 &
pid=$!
for _ in $(seq 50); do
  curl -fsS "http://127.0.0.1:$port/readyz" > /dev/null 2>&1 && break
  sleep 0.2
done
if ! curl -fsS "http://127.0.0.1:$port/readyz" > /dev/null; then
  echo "the upgraded server is not ready:"; cat "$work/serve.log"; exit 1
fi
# The channel secret written by the old release still opens (CheckSecrets on start).
if grep -q "stored secrets" "$work/serve.log"; then
  echo "secrets do not open after the upgrade:"; cat "$work/serve.log"; exit 1
fi
echo "ok: $(services "$work/after.txt" | wc -l | tr -d ' ') services survived the upgrade from v$from"
