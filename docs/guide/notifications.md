# Notifications

Notifications have two parts: **channels** (where messages go) and **rules** (which events go there, and how
often). Both are managed on the Notifications page or with the CLI.

## Channels

| Type | Settings |
| --- | --- |
| Slack | An incoming webhook URL. |
| Webhook | A URL and an optional signing secret. |
| E-mail | Recipients. Needs SMTP on the server (`GOLIASH_SMTP_ADDR`, `GOLIASH_SMTP_FROM`). |

```sh
goliash channel create -type slack -name ops -url https://hooks.slack.com/services/…
goliash channel create -type webhook -name ci -url https://example.com/goliash -secret s3cret
goliash channel create -type email -name oncall -to oncall@example.com
goliash channel test -name ops
```

Channel URLs and secrets are encrypted at rest and never shown in full in the UI.

## Rules

A rule sends some event types to a channel, optionally only for some services, owners or environments, or only for
releases of at least a given size.

```sh
goliash notify create -channel ops -events new_release,drift_detected,agent_stale -mode daily -min-jump minor
goliash notify create -channel oncall -events drift_detected -envs prod -mode instant
```

| Mode | Delivery |
| --- | --- |
| `instant` | Within seconds. Items arriving together are sent as one message. |
| `daily` | A digest every day at `digest_hour` (UTC, default 8). |
| `weekly` | A digest every Monday at `digest_hour`. |

A release is announced once per service and version. Failed deliveries are retried with backoff, from one minute up
to an hour between attempts. Acknowledged releases and drift are not sent; see
[Acknowledging](versions.md#acknowledging).

## Webhook payload

```json
{
  "workspace": "Default",
  "digest": false,
  "items": [{
    "type": "new_release",
    "service": "payments-api",
    "owner": "team-payments",
    "from": "1.6.0",
    "to": "1.7.0",
    "note": "minor",
    "at": "2026-10-03T08:00:00Z",
    "text": "payments-api: new release 1.7.0 (minor), running 1.6.0",
    "url": "https://github.com/acme/payments-api/releases/tag/v1.7.0"
  }]
}
```

`environment`, `target` and `url` are present when they apply. With a secret, the request carries
`X-Goliash-Timestamp` and `X-Goliash-Signature: sha256=<hex>`, where the hex is
`HMAC-SHA256(secret, timestamp + "." + body)`. Check the signature and reject old timestamps:

```python
import hashlib, hmac, time

def verify(secret: bytes, timestamp: str, body: bytes, signature: str) -> bool:
    if abs(time.time() - int(timestamp)) > 300:
        return False
    expected = hmac.new(secret, timestamp.encode() + b"." + body, hashlib.sha256).hexdigest()
    return hmac.compare_digest("sha256=" + expected, signature)
```

## Metrics instead of messages

If you alert from Prometheus, scrape `GET /metrics` with an API token instead:

| Metric | Labels | Value |
| --- | --- | --- |
| `goliash_deployed_version_info` | service, environment, version | running replicas |
| `goliash_outdated` | service, environment | 1 when behind upstream by the tracked jump |
| `goliash_drift_days` | service, environment, kind | days a drift has been open |
