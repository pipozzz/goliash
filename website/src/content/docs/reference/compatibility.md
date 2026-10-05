---
title: Versions and compatibility
description: 'What Goliash 1.x keeps stable: the REST API, the agent protocol, MCP tools, CLI, configuration, metrics and the database, and how deprecations and support work.'
---

Goliash follows [semantic versioning](https://semver.org/) from 1.0. Within a major version (1.x), what is listed
here keeps working; new things may be added. Anything else, such as the look of the web UI, may change in any
release.

## What stays stable in 1.x

| Interface | Promise |
| --- | --- |
| REST API `/api/v1` ([OpenAPI](/reference/api/)) | No endpoint, parameter or response field is removed or changes meaning. New endpoints, parameters and fields may appear: ignore fields you do not know. |
| Agent protocol `/agent/v1` | A server accepts agents of the same or an older 1.x release (and agents from 0.3 on). Upgrade the server first, then the agents, at your pace. |
| MCP tools | Tool names and arguments stay; new tools and optional arguments may appear. |
| CLI | Commands and flags keep working; output meant for people may change, CSV and JSON output keep their columns and fields. |
| Configuration | Environment variables, Helm chart values and Nomad pack variables keep their meaning and defaults. |
| Metrics | Names and labels of `/metrics` series stay; new series may appear. |
| Webhook payloads | Fields stay; new fields may appear. The signature scheme stays. |
| Database | Migrations only go forward. Any 1.x or 0.x database upgrades by starting a newer server. |

## Deprecations

Something to be removed is first marked deprecated: in the release notes, in these docs, and with a warning in the
server log when it is used. It keeps working for at least one more minor release, and is only removed in the next
major version (2.0).

## Upgrades and support

- Upgrade by starting the newer image or binary; take a [backup](/install/#backups) first. Downgrades are not
  supported: restore the backup instead.
- Every change is tested by upgrading databases written by the first and the latest release, on SQLite and
  PostgreSQL, and by agents of an older release reporting to the new server.
- The latest minor release gets fixes. Security fixes go to the latest minor release; report vulnerabilities
  privately as described in [SECURITY.md](https://github.com/pipozzz/goliash/blob/main/SECURITY.md).
- Releases are signed: see [Verify a release](/security/#verify-a-release).
