---
title: Grafana and SigNoz
description: 'Scrape Goliash /metrics with Prometheus or the OpenTelemetry Collector, import the ready-made Grafana and SigNoz dashboards, put deploys on your graphs and alert on drift.'
---

Goliash fits next to the observability stack you already have in three ways:

1. **Metrics.** `/metrics` serves what runs where, drift, delivery speed, image hygiene and Goliash's own health in
   the Prometheus text format. Prometheus, Grafana Agent/Alloy, VictoriaMetrics and the OpenTelemetry Collector
   (for SigNoz, Grafana Cloud, Datadog…) all read it.
2. **Dashboards.** Ready-made dashboards for Grafana and SigNoz read those metrics.
3. **Deploys on your graphs.** A deploy shows up as a marker on the graphs of the services it may have changed, from
   the metrics alone or from a Grafana notification channel.

The quickest way is **Settings → Integrations** in Goliash: it creates a viewer token with one click and shows the
scrape settings for Prometheus and the OpenTelemetry Collector, filled in for your server, next to download links
for both dashboards. The steps below do the same by hand.

## 1. A token for the scraper

`/metrics` needs an API token with the viewer role. Create one under **Users → API tokens**, or:

```sh
goliash token create -name metrics -role viewer
```

Save the `glsh_api_…` value in a file the scraper can read, e.g. `/etc/prometheus/goliash-token`.

## 2. Scrape

### Prometheus (and Grafana)

```yaml
scrape_configs:
  - job_name: goliash
    scrape_interval: 60s
    scheme: https
    authorization:
      credentials_file: /etc/prometheus/goliash-token
    static_configs:
      - targets: [goliash.example.com]
```

Grafana Alloy and Grafana Cloud take the same settings in a `prometheus.scrape` block.

### OpenTelemetry Collector (SigNoz)

Add a Prometheus receiver to the collector that sends to SigNoz, in its own pipeline:

```yaml
receivers:
  prometheus/goliash:
    config:
      scrape_configs:
        - job_name: goliash
          scrape_interval: 60s
          scheme: https
          authorization:
            credentials_file: /etc/otelcol/goliash-token
          static_configs:
            - targets: [goliash.example.com]

service:
  pipelines:
    metrics/goliash:
      receivers: [prometheus/goliash]
      processors: [batch]
      exporters: [otlp]   # the exporter your SigNoz collector already uses
```

In the SigNoz Docker or Helm install, this goes into the `signoz-otel-collector` configuration
(`otel-collector-config.yaml`, or `otelCollector.config` in the Helm values). Mount the token file into the collector
container. On SigNoz Cloud, run a collector of your own with the ingestion key, or add the receiver to the one you
already run. The metrics keep their Prometheus names and labels.

## 3. Import the dashboard

| | File | How |
|---|---|---|
| Grafana | [`deploy/grafana/goliash-dashboard.json`](https://github.com/pipozzz/goliash/blob/main/deploy/grafana/goliash-dashboard.json) | **Dashboards → New → Import**, upload the file, pick the Prometheus data source |
| SigNoz | [`deploy/signoz/goliash-dashboard.json`](https://github.com/pipozzz/goliash/blob/main/deploy/signoz/goliash-dashboard.json) | **Dashboards → New dashboard → Import JSON** |

Both have the same parts, filterable by environment and service:

- **Versions and drift:** services behind upstream, open drifts and the oldest, what runs where, services running
  more than one version.
- **Delivery (last 30 days):** deploys, the median and slowest lead time from one environment to the next, deploys
  per service and the lead time table. Lead time needs at least two environments; with only one, its panels stay
  empty (SigNoz says it cannot find `goliash_lead_time_seconds`).
- **Image hygiene:** moving tags, tags pushed again, untrusted registries and images without a digest.
- **Goliash itself:** agents online and stale, snapshots waiting, failing notifications, the server running the
  background work and the running version.

## 4. Deploys on your graphs

**From the metrics (Grafana).** The Goliash dashboard has a *Deploys* annotation that marks every new version, read
from `goliash_deployed_version_info`. To get the same markers on any other dashboard, add an annotation query on the
same Prometheus data source:

```promql
count by (service, environment, version) (
  goliash_deployed_version_info{environment="prod"} unless goliash_deployed_version_info offset 10m
)
```

with *Title* `{{service}} {{version}}` and *Tags* `environment,service`.

**From Goliash events (Grafana).** A [Grafana channel](../notifications/#deploys-on-your-dashboards) writes every event
as an annotation tagged `goliash`, the event type, the service and the environment, at the moment Goliash saw it.
The Goliash dashboard has a *Goliash events* annotation for them, off by default.

**SigNoz** has no annotations; put the *Deploys per service* panel next to your service's latency or error panels,
or alert on new versions (below).

## 5. Alerts

Worth having, in Prometheus rules, Grafana alerting or SigNoz alerts:

| Alert | Query |
|---|---|
| Behind upstream in prod | `sum(goliash_outdated{environment="prod"}) > 0` |
| Drift open for a week | `max(goliash_drift_days) > 7` |
| Agent stopped reporting | `goliash_agents{status="stale"} > 0` |
| Goliash falls behind | `goliash_snapshots_pending > 20` for 10 minutes |
| Notifications failing | `goliash_notifications_failing > 0` |
| Background work not running exactly once | `sum(goliash_leader) != 1` |

Alerts about versions themselves (new releases, drift, removed services) are better sent by Goliash directly; see
[Notifications](../notifications/).

## Metrics

| Metric | Labels | Value |
|---|---|---|
| `goliash_deployed_version_info` | service, environment, version | running replicas |
| `goliash_outdated` | service, environment | 1 when behind upstream by the tracked jump |
| `goliash_drift_days` | service, environment, kind (and app) | how long a drift has been open, in days |
| `goliash_deploys` | service, environment | versions that arrived in the last 30 days |
| `goliash_lead_time_seconds` | service, from, to | median time a version took to the next environment, last 30 days |
| `goliash_image_hygiene_findings` | kind | images with a moving tag, a tag pushed again, an untrusted registry or no digest |
| `goliash_build_info` | version | 1 |
| `goliash_leader` | | 1 on the server that runs the background work |
| `goliash_snapshots_pending` | | snapshots received and not processed yet |
| `goliash_notifications_queued`, `goliash_notifications_failing` | | notifications not sent yet, and those that failed at least once |
| `goliash_agents` | status | agents online, stale, never connected or revoked |
