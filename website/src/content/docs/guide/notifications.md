---
title: Notifications
description: 'Send new releases, drift and stale agents to Slack, Microsoft Teams, Google Chat, Discord, Telegram, ntfy, Grafana, webhooks, e-mail or browsers, instantly or as digests.'
---

Notifications have two parts: **channels** (where messages go) and **rules** (which events go there, and how
often). Both are managed on the Notifications page or with the CLI.

![The Notifications page: channels (e-mail, Microsoft Teams, web push with two browsers, Slack), rules with their time zones and quiet hours, and recent deliveries with one held for quiet hours, one in a digest and one that gave up with the mail server's answer](../../../assets/notifications.png)

## Channels

| Type | Settings |
| --- | --- |
| Slack | An incoming webhook URL. Mattermost and Rocket.Chat incoming webhooks take the same messages. |
| Microsoft Teams | The URL of a Teams workflow: in the channel, **Workflows** → *Post to a channel when a webhook request is received*. |
| Google Chat | A space's webhook URL: in the space, **Apps & integrations** → **Webhooks**. |
| Discord | A channel webhook URL (Channel settings → Integrations → Webhooks). |
| Telegram | A bot token from @BotFather and the chat ID the bot posts to. |
| ntfy | A topic URL on ntfy.sh or your own server, e.g. `https://ntfy.sh/my-goliash`, and an optional access token. |
| Grafana | Grafana's URL and a service account token with `annotations:write`; every item becomes an annotation. |
| Webhook | A URL and an optional signing secret. |
| E-mail | Recipients, and optionally the channel's own mail server (see below). |
| Web push | Nothing: each person adds their browsers from the Notifications page (see below). |
| In Goliash | Nothing: messages only show under the bell (see below). |

```sh
goliash channel create -type slack -name ops -url https://hooks.slack.com/services/…
goliash channel create -type webhook -name ci -url https://example.com/goliash -secret s3cret
goliash channel create -type discord -name releases -url https://discord.com/api/webhooks/…
goliash channel create -type teams -name platform -url https://prod-00.westeurope.logic.azure.com/workflows/…
goliash channel create -type gchat -name sre -url "https://chat.googleapis.com/v1/spaces/…/messages?key=…&token=…"
goliash channel create -type telegram -name team -token 123456:ABC… -chat-id -1001234567890
goliash channel create -type ntfy -name phone -url https://ntfy.sh/my-goliash
goliash channel create -type email -name oncall -to oncall@example.com
goliash channel test -name ops
```

Channel URLs and secrets are encrypted at rest and never shown in full in the UI.

### E-mail

Mail goes through the server's relay (`GOLIASH_SMTP_*`, see [Configuration](/reference/configuration/#e-mail)), or
through the channel's own mail server: open **Own mail server** when adding the channel, or edit it later, and give
the server as `host:port`, the sender address, and a user name and password if it needs them. Encryption is TLS on port
465 and STARTTLS elsewhere unless you pick one; *None* is for a relay on the same host or network. The password is
encrypted with the other channel secrets and never shown again; leave it empty when editing to keep it. **Send test**
checks the whole path. Without a relay on the server, an e-mail channel needs its own.

```sh
GOLIASH_CHANNEL_SMTP_PASSWORD=… goliash channel create -type email -name oncall -to oncall@example.com \
  -smtp-addr smtp.example.com:587 -smtp-from goliash@example.com -smtp-username goliash
```

### Web push

A **Web push** channel sends to browsers: desktop notifications in Chrome, Edge, Firefox and Safari, and on phones.
Add the channel, then press **Notify this browser** next to it in every browser that should get its notifications
(the browser asks for permission once); **Stop on this browser** takes it off again. Anyone with access to the
workspace can add their own browsers; the channel shows how many are on it. Rules pick the events as for any other
channel, so a channel for releases and another for end-of-life drift each go only where they were added.

A notification shows the item's kind, service and change, and opens Goliash on that service when clicked; digests
show the first lines. Browsers that unsubscribed or expired are forgotten at the next message.

- Goliash needs HTTPS (or `localhost`) for browsers to allow push; `GOLIASH_PUBLIC_URL` should be that address.
- On iPhone and iPad (iOS 16.4 or later), add Goliash to the home screen first (Share → Add to Home Screen), then
  open it from there and press **Notify this browser**.
- Apple's push service (iPhone, iPad, Safari) only accepts pushes from a server that says who it is: an https
  `GOLIASH_PUBLIC_URL` (or `GOLIASH_PUSH_SUBJECT=mailto:you@example.com`). Without one the Notifications page says
  so, and Apple devices get nothing while Chrome, Edge and Firefox do.
- **Send test** reports every browser: which got it, which did not and what its push service answered (for example
  `web.push.apple.com answered 403: BadJwtToken`), and which had unsubscribed.
- Messages are encrypted for each browser (RFC 8291) and signed with a key pair the server makes on first use and
  keeps with the other secrets (encrypted at rest with `GOLIASH_SECRET_KEY`). The server sends only to the push
  services of browsers (Google, Mozilla, Apple, Microsoft).

## The bell

The bell at the top of every page lists what Goliash sent you in the last 30 days, newest first, like the
notifications of any app. Each message has its title, its first lines and a link to where it points: the upgrade
plan opens **Updates**, an alert about one service opens that service, the rest open **History**.

- The red count on the bell is how many you have not opened yet; each person has their own. Opening a message reads
  that one only, so nothing slips by unseen in a long list; **Mark all read** reads them all.
- Every rule counts: a message sent to Slack, e-mail and push shows once. To have messages only in Goliash, add an
  **In Goliash** channel and point rules at it.
- Installed as an app (**Install** in Chrome or Edge, **Add to Home Screen** on iPhone), the app icon carries the
  same count, and a web push updates it even with Goliash closed.
- The count updates live while a page is open; **Send plan now** shows up there too.
- With [Goli](/guide/goli/) on, an alert or an upgrade plan has **Ask Goli**: Goli opens with a question about it ready
  to send (what the alert means and what to do first, or which upgrades come first). Nothing is asked until you
  press **Ask**.

## How messages look

Every item carries its kind, as an emoji and a colour: a new release ✨, drift ⚠️, an end of life ⛔, a resolved
drift ✅, an update ⬆️ coloured by urgency, a silent agent 🔌. With `GOLIASH_PUBLIC_URL` set, messages link back:
each item opens the matrix filtered to its service, and a button opens Goliash (the Updates page for an upgrade plan).

- **Slack** gets Block Kit: a header and a count by kind for digests, one section per item with the versions
  (`1.5.0` → `1.6.0`), a *Release notes* button, and *Open in Goliash* at the end; up to 20 items, then a count.
- **Discord** gets one coloured embed per item (up to ten; longer digests stay a list).
- **Microsoft Teams** gets an Adaptive Card: the title and count, one container per item in the kind's colour
  (attention, warning, good) with the versions, target and team, a *Release notes* button, and *Open in Goliash*.
- **Google Chat** gets a card with the logo in its header, one line per item with its kind, the versions in colour and
  a button (release notes, or the service in Goliash), and *Open in Goliash* at the bottom.
- **E-mail** is HTML with a plain-text alternative, in the logo's colours, readable in any mail client.
- **Web push** shows the kind's emoji and the service as the title, and the change as the text.

![A digest e-mail: a navy header with the count by kind, one card per item with a coloured edge, the versions and a button back to Goliash](../../../assets/notify-email.png)

## Rules

A rule sends some event types to a channel, optionally only for some services, owners (teams), applications or
environments, or only for releases of at least a given size. Applications are matched by the names the matrix shows
(after renames and merges, see [Applications and teams](/guide/apps/)): a rule for `webshop` gets every service
running in it, and a drift only when it is in that application.

A rule that sends **stale agents** (an agent silent for 10 minutes) also sends the all clear when the agent is back:
✅ *agent eu-cluster is back after about 25 min; its targets report again*.

```sh
goliash notify create -channel ops -events new_release,drift_detected,agent_stale -mode daily -min-jump minor
goliash notify create -channel oncall -events drift_detected -envs prod -mode instant
```

| Mode | Delivery |
| --- | --- |
| `instant` | Within seconds. Items arriving together are sent as one message. |
| `daily` | A digest every day at the digest hour (**Digest at**, default 08:00). |
| `weekly` | A digest every Monday at the digest hour. |

The digest hour is in the rule's **time zone** (any IANA name, such as `Europe/Bratislava`; UTC when empty), so
08:00 stays 08:00 across daylight saving time.

**Quiet hours** keep right-away rules from waking people: what comes in during them is held and sent together, as one
message, when they end (22:00–07:00 overnight, or 09:00–17:00 for a channel only read after work). Combine them with a
web push channel for a phone that buzzes only by day.

```sh
goliash notify create -channel phones -events drift_detected,agent_stale -timezone Europe/Bratislava -quiet 22-7
goliash notify create -channel ops -mode daily -digest-hour 9 -timezone America/New_York
```

Admins **edit** a channel (secrets are never shown again; an empty field keeps the stored value). Members **edit** a
rule (its channel, events, mode, digest hour and filters), **pause** it (it queues nothing until resumed) or delete
it on the Notifications page; admins delete a channel together with its rules. An edit applies to what comes next:
notifications already queued keep their channel and time.

A release is announced once per service and version. Failed deliveries are retried with backoff, after 1, 2, 4, 8 and
16 minutes, then given up; see [Recent deliveries](#recent-deliveries). Acknowledged releases and drift are not sent; see
[Acknowledging](/guide/versions/#acknowledging).

## Recent deliveries

The bottom of the Notifications page lists the last 25 notifications and how each went: **sent**, waiting **in the
digest** until its hour, **attempt N failed** with the error and when it is tried next (after 1, 2, 4, 8 and 16 minutes),
or **gave up after 6 attempts**. **Retry now** sends a failed one at once and says whether it went through; **Send
now** sends one waiting for its digest. A wrong webhook URL or a revoked Slack hook shows here with what the
service answered.

## Upgrade plan

A rule with the event type `updates_plan` (*upgrade plan* in the form) sends the [Updates](/guide/versions/#updates)
list at its digest time: every day for a daily rule, on Mondays for a weekly (or instant) one. It is one message, most
urgent first: what runs, the version to move to, why, and the release notes. Updates put off with an acknowledgement
are left out, and the rule's services, owners and environments apply, so each team can get its own plan:

```sh
goliash notify create -channel team-payments -events updates_plan -owners team-payments -mode weekly
```

**Send plan now** on the rule sends its plan at once, to check the channel and the filters without waiting for
Monday; the scheduled plans are not affected.

With [Goli](/guide/goli/) on and `GOLIASH_AI_PLAN_SUMMARY=true`, the plan opens with **Goli's take**: a few sentences
on what to upgrade first and why, written from the plan's own lines (one model call per plan and channel, no other
data). When the model cannot be reached, the plan goes without it.

With `GOLIASH_AI_DIGEST_SUMMARY=true`, daily and weekly digests of three or more events open with Goli's take too:
what in them needs someone's attention and what is routine, or that nothing does.

> Goli's take: Upgrade payments-api in prod first: its log4j-core is exploited in the wild and 2.16.0 fixes it.
> Postgres 15 reaches end of life next month; the minor updates in staging can wait.

## Security posture report

A rule with the event type `posture_report` (*security posture report* in the form) sends the
[Security page](/guide/security/) as one message at the same times as the plan: daily, or on Mondays. It is the
weekly line a security officer follows: how many production services run behind a newer release, the median and
90th percentile of days behind, how many are 30+ and 90+ days behind, end of life passed and near, accepted risk,
blind spots, the three longest exposures, and the same numbers a week before. It links to the Security page and, with Goli on, offers **Ask Goli**.

```sh
goliash notify create -channel ciso -events posture_report -mode weekly
```

> 🛡️ Goliash security posture for Default<br>
> prod runs 24 services, 6 behind a newer release: median 41 days, 90th percentile 212; 4 behind 30+ days, 2 behind
> 90+. End of life: 1 passed, 1 soon. Accepted risk: 2. Longest: keycloak 24.0.5 → 26.0.0 (212 days); …

It covers production, so the rule's filters do not apply, and a rule for every event does not get it: ask for it by
name. **Send report now** on the rule sends it at once. For trends over time, the same numbers are
[metrics](/guide/observability/).

## Ask Goli about an alert

With [Goli](/guide/goli/) on, a single alert (a vulnerability, a drift, an upgrade, a silent agent) carries an
**Ask Goli** button in Slack, Teams and e-mail. It opens Goliash with Goli's panel and a question about that alert
ready, e.g. *Explain this vulnerability alert for us, what fixes it and how urgent it is*; pressing **Ask** sends it.
Opening the link alone asks nothing, so link previews and stray clicks cost nothing.

## Vulnerabilities

The `vulnerability` event announces a vulnerability exploited in the wild (CISA KEV) or rated critical as soon as it
runs somewhere, once per service and environment, with the package and the version that fixes it:

> payments-api @ prod: CVE-2021-44228 (exploited, ransomware, critical) in log4j-core 2.14.1, fixed in 2.15.0

```sh
goliash notify create -channel security -events vulnerability -envs prod -mode instant
```

The first time a workspace's SBOMs are looked up completely, what already runs is recorded without announcing, so
an installation does not hear about every vulnerability it runs on day one; the Security page lists those. See
[Security posture](../security/#is-it-running).

## Deploys on your dashboards

A Grafana channel turns events into annotations, so a deploy shows up on the graphs it may have changed:

```sh
goliash channel create -type grafana -name dashboards -url https://grafana.example.com -token glsa_…
goliash notify create -channel dashboards -events deployed,version_changed,removed -envs prod -mode instant
```

Annotations are tagged `goliash`, the event type, the service and the environment. In a dashboard, add an
annotation query on the built-in Grafana data source, filtered by tags (`goliash`, and for example `prod`).

## What changed before an incident

The History page has a *changed in the last hour / 6 hours / 24 hours / 7 days* filter, and the same works from
the command line and the API:

```sh
goliash events -env prod -since 2h
curl -H "Authorization: Bearer $GOLIASH_TOKEN" "https://goliash.example.com/api/v1/events?environment=prod&since=2h"
```

## Webhook payload

```json
{
  "workspace": "Default",
  "digest": false,
  "link": "https://goliash.example.com",
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

`environment`, `target` and `url` are present when they apply; `link` is the server's public URL. With a secret, the request carries
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
| `goliash_drift_days` | service, environment, kind (and app, for a service compared per application) | days a drift has been open |
| `goliash_deploys` | service, environment | versions that arrived in the last 30 days |
| `goliash_lead_time_seconds` | service, from, to | median time a version took to the next environment, last 30 days |
| `goliash_image_hygiene_findings` | kind | images with a moving tag, a tag pushed again, an untrusted registry or no digest |
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

Alerts worth having on Goliash itself: `goliash_agents{status="stale"} > 0`, `goliash_snapshots_pending > 20` for
ten minutes, `goliash_notifications_failing > 0`, and `sum(goliash_leader) != 1` across servers.

```yaml
scrape_configs:
  - job_name: goliash
    scheme: https
    authorization: { credentials_file: /etc/prometheus/goliash-token }
    static_configs: [{ targets: ["goliash.example.com"] }]
```

Ready-made Grafana and SigNoz dashboards, scrape settings for the OpenTelemetry Collector, deploy markers on
your graphs and more alerts are on [Grafana and SigNoz](../observability/).
