---
title: Matrix and mapping
---

## The matrix

The matrix has one row per **service** and one column per **environment**, in promotion order (dev, staging,
prod). Each cell shows the versions running there, with replicas and the targets they run on. The last column shows
the newest upstream version the service's policy accepts.

Badges in a cell:

| Badge | Meaning |
| --- | --- |
| behind *env* | This environment runs an older version than the one before it. |
| upstream *x.y.z* | A newer release exists, at least as large a jump as the policy tracks. |
| targets disagree | Targets of this environment run different versions. |
| stale data | The agent stopped sending heartbeats, or there has been no snapshot for three poll intervals (at least 15 minutes). |

During a rollout, both versions show with their replica counts. The matrix updates live as snapshots arrive.

## Services, environments and targets

- A **target** is one thing a collector reads: a Kubernetes cluster, an ECS region, a Nomad region, a Swarm or a
  Docker host. Each target belongs to one environment.
- An **environment** has a position that sets the promotion order: lower comes first (dev 10, staging 20, prod 30).
- A **service** is what you deploy, wherever it runs. Running workloads are mapped to services.

A workload can override its environment with the label `goliash.env`, for example when one cluster hosts both
staging and prod namespaces.

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

Patterns are regular expressions that must match the whole value.

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
