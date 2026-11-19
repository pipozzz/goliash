---
title: Goliash and the tools you already have
description: 'What Goliash adds next to Renovate, Dependabot, Trivy, Dependency-Track, Argo CD and a service catalog — and where each of them is the better tool.'
---

Most tools that deal with versions start from **what a repository says**. Goliash starts from **what runs**: it
asks the orchestrators, on every platform you have, and works back from there. That one difference is why it sits
next to the tools below instead of replacing them.

| | Starts from | Answers | Does not know |
|---|---|---|---|
| **Renovate, Dependabot** | A repository's manifests | *What can this repo upgrade?* — and opens the pull request | Whether the merge was deployed, what runs where no repo pins it, what staging runs against prod |
| **Trivy, Grype** | An image or a cluster you point it at | *Which CVEs are in this image?* | How long it has run, where else it runs, whether the fix is out upstream and already merged |
| **Dependency-Track** | SBOMs your builds upload | *Which components are in what we built?* | Which of those builds runs today, and in which environment |
| **Argo CD, Flux** | Git, one cluster at a time | *Is this cluster what Git says?* | Whether what Git says is current, and the clusters and platforms it does not manage |
| **Backstage and other catalogs** | What teams write down | *Who owns this service?* | The version that runs, unless someone keeps it up to date |
| **Goliash** | The runtime, on every platform | *What runs where, on which version, how far behind, since when — and did the fix reach production?* | Your source code: libraries in a repo that is not built into a running image |

## Three things only the runtime can tell

1. **One picture across platforms.** Kubernetes, ECS, Lambda, Nomad, Swarm, Compose hosts, Linux servers and
   managed databases in one service × environment matrix. A scanner or a GitOps tool sees its own platform.
2. **A merge is not a deploy.** Goliash follows a version from its release, through the pull request, to the
   moment it runs — and says so when a merged fix does not run, with what holds it (a sync window, a manual
   sync, a suspended Kustomization).
3. **Time.** Because it watches continuously, it knows how long a vulnerable version ran in production and when
   it stopped. That is the evidence an audit asks for (NIS2, DORA, SOC 2, ISO 27001), and no point-in-time scan
   has it.

## Use them together

- **Renovate or Dependabot** keep opening pull requests; Goliash links theirs instead of opening its own, and
  shows which of them reached production. Where nothing opens pull requests — a Helm value, an ECS task
  definition, a Nomad job — Goliash can.
- **Trivy** in CI still fails a build. Goliash tells you whether that image, or an older one with the same
  finding, runs, and where.
- **Dependency-Track** takes Goliash's CycloneDX export: the SBOM of what runs, per environment, instead of the
  SBOM of everything ever built.
- **Argo CD and Flux** do the deploying. Goliash reads their applications next to what runs and never syncs
  anything itself.

## When Goliash is not the tool

- You want dependency updates **inside your code** (`package.json`, `go.mod`): that is Renovate's or
  Dependabot's job.
- You want to **block a build or an admission** on a finding: use a scanner in CI or an admission controller.
  Goliash only reads.
- You run **one cluster with Argo CD and nothing else**, and its UI already answers your questions.

To see the difference on your own cluster in a minute, with nothing installed, run
[`goliash scan`](/reference/cli/#scan-your-own-data-nothing-installed).
