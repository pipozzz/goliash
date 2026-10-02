#!/usr/bin/env bash
# Prepends the copyright and SPDX header to generated Go files.
# Usage: spdx-header.sh <SPDX-ID> <file>...
set -euo pipefail

license=$1
shift
for f in "$@"; do
  tmp=$(mktemp)
  printf '// Copyright 2026 The Goliash Authors\n// SPDX-License-Identifier: %s\n\n' "$license" > "$tmp"
  cat "$f" >> "$tmp"
  mv "$tmp" "$f"
done
