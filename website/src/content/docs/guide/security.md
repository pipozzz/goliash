---
title: Security posture
description: 'How long production runs behind available releases, what is past its end of life, supply-chain findings and blind spots, with a CSV and PDF for risk registers and audits.'
---

The **Security** page answers the questions a security officer asks about what runs, from the runtime itself rather
than from tickets or pipelines:

- **How long has production been exposed?** For every service behind a newer release, the time since the *first*
  newer release came out. The median and the 90th percentile summarise it, with the count behind for more than 30
  and 90 days.
- **What is past its end of life?** Release cycles whose support has ended, or ends within 60 days
  ([endoflife.date](https://endoflife.date)).
- **What risk has been accepted?** Upgrades quieted with an acknowledgement count as accepted risk, with who
  acknowledged them and until when (on the service page).
- **Can the images be trusted?** Moving tags, tags pushed again, registries outside the allowed list and images
  without a known digest, from [image hygiene](../matrix/#image-hygiene).
- **Where are we blind?** Targets that stopped reporting, targets that report no digests, and services whose newest
  release is unknown, so their exposure cannot be measured.

## Base images

A service's own image is only as supported as the image it is built on: `node:18-alpine` reached its end of life in
April 2025 whatever the application's version. Goliash reads the base from the
`org.opencontainers.image.base.name` annotation or label, which BuildKit (`docker buildx`) records, when it reads
the image's source repository. The service page shows it under **Built on**, with its end of life from
[endoflife.date](https://endoflife.date), and the Security page lists every running service **built on an
unsupported base**: past its end of life, or within 60 days of it.

An image that does not declare its base can name it in the service's version policy (**Base image**, for example
`node:22-alpine`); `none` turns tracking off. The image is read on a public registry by the server; for private
images, set the base in the policy.

## Supply-chain evidence

For every image running in production, Goliash asks its registry what it holds beside the image:

- **Signed**: a [cosign](https://docs.sigstore.dev/cosign/) signature (`sha256-<digest>.sig`), a Sigstore bundle or a
  Notation signature as an OCI referrer.
- **SBOM**: an SPDX or CycloneDX attestation, as `docker buildx build --sbom=true` attaches it, or an SBOM artifact.
- **Provenance**: a SLSA provenance attestation (`--provenance=true`, GitHub artifact attestations).

The Security page shows the share of production images with each, and lists the unsigned ones. Goliash records that
these **exist**; it does not verify who signed them. Images on public registries are looked up by the server, at
most 40 per workspace and check, again after a week when something was found and after a day when nothing was.
Images on private registries, and images without a known digest, count as *not checked*.

## How exposure is counted

Exposure starts when the first release newer than the running one was **published** (GitHub or GitLab release
dates). When a release's date is unknown, Goliash counts from when it first saw the release and shows the number as
a lower bound (`≥ 12 days`); such rows are left out of the median and the percentile. A service running several
versions in one environment counts with its oldest, the one an upgrade has to reach, and names the targets running
it.

The page shows the last environment (production) by default; *Every environment* lists all of them.

## For audits

- **Export CSV** gives every exposure with its service, application, environment, targets, running version, the
  version to move to, the exposure date and days, whether the date is known, end of life, accepted risk and the
  release notes link: a ready start for a risk register.
- **Print or save as PDF** prints the page with the time it was generated, as evidence of patch management for
  frameworks that ask for it (NIS2, DORA, SOC 2, ISO 27001).

The same data is available from the [REST API](../../reference/api/) and to AI assistants over
[MCP](../../reference/mcp/).
