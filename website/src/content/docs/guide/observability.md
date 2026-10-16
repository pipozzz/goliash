---
title: Metrics and alerts
description: 'Goliash /metrics for Prometheus and the OpenTelemetry Collector: what runs where, drift, delivery speed and security posture, ready-made Grafana and SigNoz dashboards, and alert rules.'
---

For a quick look without any of that, **Settings → Status** (also the *Live* sign above the matrix) says whether
Goliash itself works and its data is fresh: agents and targets reporting, upstream releases checked, private
registries accepting their credentials, SBOMs, OSV and CISA's catalog current, notifications going out, and whether a
newer Goliash is out. Each line
names what is wrong and links to where to fix it. The same is `GET /api/v1/status` for an uptime monitor (alert when
`state` is `bad`) and the `status` tool for AI assistants.

Goliash fits next to the observability stack you already have in three ways:

1. **Metrics.** `/metrics` serves what runs where, drift, delivery speed, image hygiene and Goliash's own health in
   the Prometheus text format. Prometheus, Grafana Agent/Alloy, VictoriaMetrics and the OpenTelemetry Collector
   (for SigNoz, Grafana Cloud, Datadog…) all read it.
2. **Dashboards.** Ready-made dashboards read those metrics:
   - [Grafana dashboard](../grafana/): Prometheus scrape settings, the dashboard, deploy markers on your graphs and
     the Helm chart's ServiceMonitor, PrometheusRule and dashboard ConfigMap.
   - [SigNoz dashboard](../signoz/): the OpenTelemetry Collector receiver and the dashboard.
3. **Deploys on your graphs.** A deploy shows up as a marker on the graphs of the services it may have changed, from
   the metrics alone or from a Grafana notification channel; see [Grafana dashboard](../grafana/#4-deploys-on-your-graphs).

The quickest way is **Settings → Integrations** in Goliash: it creates a viewer token with one click and shows the
scrape settings for Prometheus and the OpenTelemetry Collector, filled in for your server, next to download links
for both dashboards.

## A token for the scraper

`/metrics` needs an API token with the viewer role. Create one under **Users → API tokens**, or:

```sh
goliash token create -name metrics -role viewer
```

Save the `glsh_api_…` value in a file the scraper can read, e.g. `/etc/prometheus/goliash-token`, and send it as a
bearer token; the [Grafana](../grafana/#2-scrape-goliash) and [SigNoz](../signoz/#2-scrape-goliash-with-the-opentelemetry-collector)
pages show the scrape settings.

## Alerts

Ready-made Prometheus rules are in
[`deploy/prometheus/goliash-alerts.yaml`](https://github.com/pipozzz/goliash/blob/main/deploy/prometheus/goliash-alerts.yaml)
(also a download on the Integrations page): add the file to `rule_files` in `prometheus.yml`. They cover a missing
scrape, the background work not running exactly once, stale agents, a growing snapshot queue, failing notifications
drift open for more than two weeks, production on a release past its end of life, production behind an available
release for 90+ days, and an image that grew by a quarter (and 20 MB) in a day. The same queries work in Grafana alerting and SigNoz alerts:

| Alert | Query |
|---|---|
| Behind upstream in prod | `sum(goliash_outdated{environment="prod"}) > 0` |
| Drift open for a week | `max(goliash_drift_days) > 7` |
| Production past end of life | `goliash_end_of_life{state="passed"} > 0` |
| Production patched slower than a month | `max(goliash_exposure_median_days) > 30` |
| Agent stopped reporting | `goliash_agents{status="stale"} > 0` |
| Goliash falls behind | `goliash_snapshots_pending > 20` for 10 minutes |
| Notifications failing | `goliash_notifications_failing > 0` |
| Background work not running exactly once | `sum(goliash_leader) != 1` |
| Image grew a lot | `goliash_image_size_bytes > 1.25 * (goliash_image_size_bytes offset 1d)` |

Alerts about versions themselves (new releases, drift, removed services) are better sent by Goliash directly; see
[Notifications](../notifications/).

On Kubernetes, the Helm chart can create a ServiceMonitor and a PrometheusRule with these alerts; see
[Grafana dashboard](../grafana/#kubernetes).

## Metrics

| Metric | Labels | Value |
|---|---|---|
| `goliash_deployed_version_info` | service, environment, version | running replicas |
| `goliash_outdated` | service, environment | 1 when behind upstream by the tracked jump |
| `goliash_drift_days` | service, environment, kind (and app) | how long a drift has been open, in days |
| `goliash_deploys` | service, environment | versions that arrived in the last 30 days |
| `goliash_lead_time_seconds` | service, from, to | median time a version took to the next environment, last 30 days |
| `goliash_image_hygiene_findings` | kind | images with a moving tag, a tag pushed again, an untrusted registry, an outdated base or no digest |
| `goliash_image_size_bytes` | service, environment | what pulling the service's image downloads, in bytes (compressed layers and config) |
| `goliash_exposure_days` | service, environment (and app) | days a service has run behind an available release |
| `goliash_exposure_median_days`, `goliash_exposure_p90_days` | environment (production) | median and 90th percentile of those days in production |
| `goliash_exposures` | environment, over_days (30, 90) | production services behind an available release that long |
| `goliash_end_of_life` | environment, state (passed, soon) | production services on a release cycle past or near its end of life |
| `goliash_accepted_risk` | environment | production exposures quieted by an acknowledgement |
| `goliash_build_info` | version | 1 |
| `goliash_leader` | | 1 on the server that runs the background work |
| `goliash_snapshots_pending` | | snapshots received and not processed yet |
| `goliash_notifications_queued`, `goliash_notifications_failing` | | notifications not sent yet, and those that failed at least once |
| `goliash_agents` | status | agents online, stale, never connected or revoked |
