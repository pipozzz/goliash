---
title: Versions and drift
---

## Where upstream versions come from

For each service, Goliash picks an **upstream repository**: the one set on the service, else the image its main
containers run. It lists that repository's tags and keeps the newest versions the policy accepts as releases.

- **Public registries** are checked by the server: Docker Hub, GHCR, Quay, registry.k8s.io, gcr.io,
  public.ecr.aws, mcr.microsoft.com, registry.gitlab.com and docker.elastic.co.
- **Every other registry** is checked by the agents, with credentials from their own environment. See
  [Collectors](/goliash/reference/collectors/#private-registries).

New services are checked within a minute, then every hour. **Check upstream now** on a service page, or
`goliash check`, checks at once.

## Version policy

A policy decides which tags count as versions and which differences are worth an alert. Set it on the service
page, with `goliash service set`, or through the catalog.

| Field | Meaning | Default |
| --- | --- | --- |
| `track` | The smallest jump worth an alert: `patch`, `minor` or `major`. | `patch` |
| `pin_major` | Stay on this major: newer majors are shown, not alerted. | none |
| `tag_filter` | A regular expression tags must match. | tags shaped like the running one |
| `prerelease` | Consider alpha, beta and rc versions. | `false` |
| `drift_alert_after` | How long drift lasts before it is announced, per kind, e.g. `{"env": "72h"}`. | see below |
| `github`, `github_tag_prefix` | GitHub repository (`owner/repo`) whose releases give dates and release notes. | from catalog or image label |
| `changelog` | A release notes URL template with `{version}`. | none |

Without a `tag_filter`, tags are compared **like with like**: a service running `1.27.2-alpine` is compared with
other `-alpine` tags with three version parts, so `latest`, `mainline` or `1.27-bookworm` do not count.

```sh
goliash service set -name postgres -track minor -pin-major 15
goliash service set -name app -tag-filter '^v\d+\.\d+\.\d+$'
```

## The catalog

The built-in [catalog](https://github.com/pipozzz/goliash/blob/main/catalog/images.yaml) has default policies for
popular public images (postgres, redis, nginx, keycloak, traefik, grafana, prometheus and more) and where their
release notes live. A service without its own policy uses the catalog's; the service page then says "from catalog".
Additions are welcome as pull requests.

## Release notes

Releases get a publication date and a release notes link from GitHub releases. The repository comes from, in order:

1. `github` in the service's own policy,
2. the catalog,
3. the image's own `org.opencontainers.image.source` label, read from the registry once a week. Most images built
   with GitHub Actions set it, so they get release notes without any setup. Docker Official Images are skipped,
   because their label points at the repository that packages them, not the project.

The service page says which one is used. Set `GOLIASH_GITHUB_TOKEN` to raise GitHub's rate limit when you track
many services.

## Drift

| Kind | Meaning | Announced after |
| --- | --- | --- |
| `env` | An environment runs an older version than the environment before it. | 7 days |
| `upstream` | The version is behind the newest release by at least the tracked jump. | immediately |
| `inconsistent` | Targets of one environment run different versions. | 15 minutes |

Drift shows in the UI as soon as it exists. It is **announced** as a `drift_detected` event, which is what
notification rules react to, only once it has lasted the time above. That way a normal promotion from staging to
prod over a few days, or a rollout in progress, does not page anyone. Override per service:

```json
{"drift_alert_after": {"env": "72h", "inconsistent": "1h"}}
```

When the drift ends, a `drift_resolved` event follows.

## Acknowledging

An acknowledgement silences notifications for a service, optionally in one environment, until a version ships or a
time passes:

```sh
goliash ack -service postgres -kind release -until-version 17.0   # "we know about 16, quiet until 17"
goliash ack -service web -kind drift -env prod -for 336h         # quiet for 14 days
```

The same works on the service page and through `POST /api/v1/acks`.
