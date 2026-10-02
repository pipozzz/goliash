# Contributing to Goliash

Thanks for your interest. Goliash is in early development, so please open an issue to discuss larger changes before
writing code.

## Contributor License Agreement

External contributions require signing the [Contributor License Agreement](CLA.md) — a DCO sign-off is not enough.
The CLA lets the project keep offering the agent under Apache-2.0 and the server under AGPL-3.0 together with the
hosted service at goliash.dev, and change licensing in the future if needed. You keep the copyright to your work.

When you open your first pull request, the CLA bot comments on it. To sign, reply with:

> I have read the CLA Document and I hereby sign the CLA

You sign once; it covers all your future contributions. If you contribute on behalf of your employer, make sure you
are authorized to do so (see section 4 of the CLA).

## Development setup

- Go — the version in `go.mod`
- [golangci-lint](https://golangci-lint.run) v2
- Docker, for integration tests against PostgreSQL and orchestrators (later steps)

```sh
make build           # bin/goliash and bin/goliash-agent
make test            # go test -race ./...
make lint            # license boundary check + golangci-lint
make fmt             # gofumpt + goimports
```

## Ground rules

- **Read-only.** Collectors only ever read. Never add code that creates, updates or deletes anything in an
  orchestrator or registry, and keep the documented permissions (RBAC, IAM, ACL) minimal.
- **License headers.** Every Go file starts with a copyright line and an SPDX header. Use `Apache-2.0` under
  `cmd/goliash-agent`, `internal/agent`, `internal/collectors`, `internal/registry` and `pkg`, and `AGPL-3.0-only`
  everywhere else. See [LICENSING.md](LICENSING.md).
- **The agent never imports server code.** CI enforces this with `scripts/check-licenses.sh`.
- **Both databases.** Schema changes need a migration with the same number in both
  `internal/store/migrations/sqlite` and `internal/store/migrations/postgres` (`TestSchemaParity` compares them),
  and every table under a workspace carries `org_id` and `workspace_id`. Run the store tests against PostgreSQL with:

  ```sh
  docker run -d --name goliash-pg -e POSTGRES_USER=goliash -e POSTGRES_PASSWORD=goliash -p 55432:5432 postgres:17-alpine
  GOLIASH_TEST_POSTGRES_DSN='postgres://goliash:goliash@localhost:55432/goliash?sslmode=disable' go test ./internal/store/
  ```
- **Tests.** New behavior comes with tests; bug fixes come with a test that fails without the fix.

## The image catalog

[`catalog/images.yaml`](catalog/images.yaml) lists public images with a default version policy and where their
release notes live. To add an image, add one entry in alphabetical order:

```yaml
- image: docker.io/library/nginx      # registry/repository as Goliash normalizes it
  github: nginx/nginx                 # GitHub releases (dates and links) …
  github_tag_prefix: release-         # … whose tags are "release-<version>"
  policy: { track: minor }            # optional default policy
```

`go test ./internal/versions/` validates every entry.

## Pull requests

- Keep each PR focused on one change.
- Use [Conventional Commits](https://www.conventionalcommits.org) for commit messages and PR titles, e.g.
  `feat(collectors/ecs): read task definition tags`.
- CI (license check, lint, tests, build) must pass.
- Update the docs when behavior changes.

## Reporting security issues

Please do not open public issues for security problems. E-mail the maintainers instead; we will acknowledge within a
few working days.
