---
title: Matrix and mapping
description: 'Read the service × environment matrix, filter it, look back in time, export the inventory and map workloads to services.'
---

## The matrix

The matrix has one row per **service** and one column per **environment**, in promotion order (dev, staging,
prod). Each cell shows the versions running there, with replicas and the targets they run on. The last column shows
the newest upstream version the service's policy accepts.

**Filter** the matrix by typing above it (press <kbd>/</kbd> to jump there): every word must appear in the service,
its owner, a version, a target or a badge, so `team-payments end` finds that team's services on an end-of-life
release. **Only with drift** hides the rest. The filter is part of the address, so a filtered matrix can be
bookmarked or shared. **Time travel** shows the matrix as of a past moment; **Export CSV** downloads the inventory.

<img src="/screenshots/mobile.png" width="260" align="right" alt="The matrix on a phone: one card per service with its versions per environment" style="margin: 0 0 1rem 1.5rem; border-radius: 12px; border: 1px solid var(--sl-color-gray-5)" />

On a phone the matrix turns into one card per service, with each environment as a row inside it, so nothing scrolls
sideways. The same filter and drift switch work there.

Badges in a cell:

| Badge | Meaning |
| --- | --- |
| behind *env* | This environment runs an older version than the one before it. |
| upstream *x.y.z* | A newer release exists, at least as large a jump as the policy tracks. |
| targets disagree | Targets of this environment run different versions. |
| stale data | The agent stopped sending heartbeats, or there has been no snapshot for three poll intervals (at least 15 minutes). |

During a rollout, both versions show with their replica counts. The matrix updates live as snapshots arrive.

## Looking back, and exporting for audits

Open **Time travel** above the matrix and pick a date and time to see what ran then: "what was in prod on 12 September at
14:00?". Goliash keeps when each version ran for 400 days, so the answer is right even after versions moved on and
back. Drift and upstream releases describe now, so they are not shown for the past.

**Export CSV** downloads every container that runs (or ran at that time), sidecars included, with environment,
target, workload, image, tag, digest, replicas and when it was first seen: an asset inventory for ISO 27001 or
SOC 2 audits. The same is available from the CLI and the API:

```sh
goliash matrix -at 2026-09-12T14:00
goliash inventory -csv > inventory.csv
goliash inventory -at 2026-09-12T14:00 -csv
curl -H "Authorization: Bearer $GOLIASH_TOKEN" "https://goliash.example.com/api/v1/inventory?format=csv&at=2026-09-12T14:00"
```

## Image hygiene

**Image hygiene** (linked above the matrix, with the number of warnings) lists running images that make "what
runs" hard to know or to trust, sidecars included:

| Finding | Meaning |
| --- | --- |
| moving tag | `latest`, `stable`, `main` or no tag: the version cannot be known, and a restart may pull something else. |
| retagged | The same repository and tag run as different images (digests): the tag was pushed again. |
| untrusted registry | The image comes from outside `GOLIASH_ALLOWED_REGISTRIES` (e.g. `ghcr.io/acme,docker.io/library`). |
| unpinned (info) | No digest is known for the image. |

`goliash hygiene`, `GET /api/v1/hygiene` and `goliash_image_hygiene_findings{kind}` in `/metrics` show the same.
Compose files are left out: they declare tags, not what runs.

## Services, environments and targets

- A **target** is one thing a collector reads: a Kubernetes cluster, an ECS region, a Nomad region, a Swarm or a
  Docker host. Each target belongs to one environment.
- An **environment** has a position that sets the promotion order: lower comes first (dev 10, staging 20, prod 30).
- A **service** is what you deploy, wherever it runs. Running workloads are mapped to services.

A workload can override its environment with the label `goliash.env`, for example when one cluster hosts both
staging and prod namespaces.

A service that runs nowhere any more can be deleted from its page by an admin, with its policy, releases, mapping
rules and acknowledgements; its history stays. Admins rename and reorder environments on the Agents page (drift is re-evaluated with the new order), and delete one
once no target belongs to it. **Edit** on a target changes its environment, settings and poll interval; its
collector picks the change up with the next configuration poll.

## How workloads map to services

Each running workload goes through these steps, and the first match wins:

1. **Ignore rules.** Images or containers you chose to ignore.
2. **Labels.** `goliash.service`, else `app.kubernetes.io/name`. The service is created when it does not exist yet.
3. **Rules.** Lowest priority number first. The inbox creates workload-name rules at priority 50 and image rules at
   100, so a rule for one workload wins over a rule for its image. `goliash rule create` defaults to 100.
4. **The inbox.** Everything else waits there with a suggested name, taken from the image.

The labels are Kubernetes labels, ECS tags, Nomad job meta, or Swarm and Docker labels.

Within a workload, the **main container** carries the version. It is the container named by the `goliash.container`
label, else one named like the workload or service. Known sidecars such as `istio-proxy`, `linkerd-proxy`, `envoy`
or `datadog-agent` are skipped.

## The inbox

The inbox groups unmapped workloads by image.

- **Map all** maps every workload running the image to a service at once and adds an image rule, so new
  workloads with that image map by themselves.
- **Map only this** is for an image that runs as several services, for example one image deployed as `web` and
  `worker`. It maps a single workload and adds a workload-name rule, which takes precedence over image rules.
- **Ignore this image** removes it from the inbox at once and stops tracking it from the next snapshot.

Rules can also be created from the CLI:

```sh
goliash rule create -match image_repo -pattern 'ghcr\.io/acme/pay.*' -service payments
goliash rule create -match workload_name -pattern 'worker' -service jobs -priority 50
goliash rule create -match label -pattern 'team=payments' -service payments
goliash rule create -match ignore -pattern 'docker\.io/library/busybox'
```

Patterns are regular expressions that must match the whole value. The inbox page lists every rule; deleting one
returns the workloads it matched to the inbox with the next snapshot, unless another rule or a label maps them.

## History

Every snapshot is compared with the previous one of the same target. The differences become events:

| Event | When |
| --- | --- |
| `deployed` | A workload appears. |
| `version_changed` | A workload runs a different tag. A changed digest under the same tag is noted as a retag. |
| `removed` | A workload disappears. |
| `new_release` | An upstream release newer than everything known appears. |
| `drift_detected`, `drift_resolved` | Drift is announced, or ends. |
| `agent_stale` | An agent stops sending heartbeats. |

The first snapshot of a target is a baseline and creates no events. When a snapshot is incomplete, for example
because one namespace was forbidden, missing workloads are not treated as removed.
