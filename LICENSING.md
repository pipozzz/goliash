# Licensing

Goliash is split between two licenses.

| Component | License | Why |
| --- | --- | --- |
| `goliash-agent` and everything it is built from | [Apache-2.0](LICENSES/Apache-2.0.txt) | The agent runs inside customers' infrastructure, so it must be auditable and free of licensing concerns. |
| `goliash` server, UI, API, migrations, deploy manifests | [AGPL-3.0-only](LICENSES/AGPL-3.0-only.txt) | Anyone offering the server as a service must publish their changes. |

The root [`LICENSE`](LICENSE) file is the AGPL-3.0 text, because it covers everything not marked otherwise.

## Which license applies to a file

Every Go source file starts with an SPDX header, which is authoritative:

```go
// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0
```

Apache-2.0 paths: `cmd/goliash-agent`, `internal/agent`, `internal/collectors`, `internal/registry`, `pkg`, and the
agent protocol spec `api/agent-v1.yaml`. Generated code gets its header from `scripts/spdx-header.sh`.
Everything else is AGPL-3.0-only.

## The boundary rule

The agent binary must never include AGPL code. `scripts/check-licenses.sh` (run in CI and by `make lint`) fails if:

- a Go file has no SPDX header, or
- any Goliash package that `./cmd/goliash-agent` depends on, directly or transitively, is not Apache-2.0.

The server may import Apache-2.0 packages freely. If the agent needs something that lives in a server package, move
that code into an Apache-2.0 package instead.
