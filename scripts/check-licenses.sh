#!/usr/bin/env bash
# Checks the license boundary between the agent (Apache-2.0) and the server (AGPL-3.0-only):
#   1. every Go file carries an SPDX-License-Identifier header;
#   2. every Goliash package the agent binary depends on is Apache-2.0.
set -euo pipefail

module=$(go list -m)
fail=0

while IFS= read -r f; do
  if ! head -n 5 "$f" | grep -qE '^// SPDX-License-Identifier: (Apache-2\.0|AGPL-3\.0-only)$'; then
    echo "missing or unknown SPDX header: $f"
    fail=1
  fi
done < <(git ls-files --cached --others --exclude-standard '*.go')

while read -r pkg dir; do
  case "$pkg" in "$module"|"$module"/*) ;; *) continue ;; esac
  for f in "$dir"/*.go; do
    [[ "$f" == *_test.go ]] && continue
    if ! head -n 5 "$f" | grep -q '^// SPDX-License-Identifier: Apache-2.0$'; then
      echo "goliash-agent depends on non-Apache-2.0 code: ${f#"$PWD"/} (package $pkg)"
      fail=1
    fi
  done
done < <(go list -deps -f '{{if not .Standard}}{{.ImportPath}} {{.Dir}}{{end}}' ./cmd/goliash-agent)

if [[ $fail -ne 0 ]]; then
  echo "license check failed; see LICENSING.md"
  exit 1
fi
echo "license check passed"
