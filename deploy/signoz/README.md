# Goliash dashboard for SigNoz

`goliash-dashboard.json` shows what runs where, open drift, services behind upstream per environment and the longest
drift per service, filterable by environment and service. It reads the metrics Goliash serves at `/metrics`.

## 1. Scrape Goliash with the OpenTelemetry Collector

Create an API token in Goliash (**Users → API tokens**, or `goliash token create -name signoz`) and add a Prometheus
receiver to the collector that sends to SigNoz:

```yaml
receivers:
  prometheus/goliash:
    config:
      scrape_configs:
        - job_name: goliash
          scrape_interval: 60s
          scheme: https
          authorization:
            credentials_file: /etc/otelcol/goliash-token   # the glsh_api_… token
          static_configs:
            - targets: [goliash.example.com]

service:
  pipelines:
    metrics/goliash:
      receivers: [prometheus/goliash]
      processors: [batch]
      exporters: [otlp]   # your SigNoz exporter
```

The metrics keep their Prometheus names and labels: `goliash_deployed_version_info{service, environment, version}`,
`goliash_outdated{service, environment}` and `goliash_drift_days{service, environment, kind}`.

## 2. Import the dashboard

In SigNoz, open **Dashboards → New dashboard → Import JSON** and paste or upload `goliash-dashboard.json`.
