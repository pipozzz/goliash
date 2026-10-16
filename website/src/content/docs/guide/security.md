---
title: Security posture
description: 'How long production runs behind available releases, what is past its end of life, supply-chain findings and blind spots, with a CSV and PDF for risk registers and audits.'
---

The **Security** page answers the questions a security officer asks about what runs, from the runtime itself rather
than from tickets or pipelines:

![The Security page: tiles for the median exposure of production, services behind for 30 or more days, end of life, accepted risk and blind spots, then each production service with its running version, the fix, and how many days it has been exposed](../../../assets/security.png)

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

### Built on an outdated base

A new build is not always a fresh one: a build cache, or `FROM` without `--pull`, keeps the base's layers as they
were when the base was first pulled, with the vulnerabilities its newer builds fixed. The image's version and date
look current; its bottom layers are not.

Goliash checks this with the layers themselves. Building `FROM node:22-alpine` copies the base's layers unchanged,
so an image built on the base as its tag is now starts with exactly those layers. When it reads a running image's
evidence, Goliash also reads the base's tag (for the same platform, once per check) and compares:

- **current**: the image starts with the base's layers as they are now;
- **outdated**: it does not: it was built on an older build of the base (or on another base than it declares).

Outdated ones show as **Outdated base** on [Image hygiene](/guide/matrix/#image-hygiene), with when the base's current build was
made (*built on an older node:22-alpine than the one its tag has now (built Oct 2, 14 days ago): rebuild with
`--pull`*), in the Security page's supply chain, as an **older base** badge on the service's images, and in
`goliash_image_hygiene_findings{kind="outdated-base"}`. Bases pinned to a digest (`node@sha256:…`) are pinned on
purpose and left alone. Agents send the layers of private images (agent 1.61); the base itself must be readable by
the server, as public bases are.

## Supply-chain evidence

For every image running in production, Goliash asks its registry what it holds beside the image:

- **Signed**: a [cosign](https://docs.sigstore.dev/cosign/) signature (`sha256-<digest>.sig`), a Sigstore bundle or a
  Notation signature as an OCI referrer.
- **SBOM**: an SPDX or CycloneDX attestation, as `docker buildx build --sbom=true` attaches it, or an SBOM artifact.
- **Provenance**: a SLSA provenance attestation (`--provenance=true`, GitHub artifact attestations).

The Security page shows the share of production images with each, and lists the unsigned ones. Goliash records that
these **exist**; it does not verify who signed them. Images on public registries are looked up by the server, at
most 40 per workspace and check, again after a week when something was found and after a day when nothing was.
Images on private registries are looked up by the agents, with the credentials they already use for private tags
(up to 20 digests per repository and round), and they send the packages of the SBOM along; this needs agent 1.27 or
newer. Images on Amazon ECR need the agent's role to have `ecr:GetAuthorizationToken`, `ecr:BatchGetImage` and
`ecr:GetDownloadUrlForLayer` (the CloudFormation and Terraform setups include them). Images without a known
digest count as *not checked*.

The same lookup reads the image's **size**: what pulling it downloads, its compressed layers and config, as
registries show it (linux/amd64, or the first platform, of a multi-platform image). A service's page lists it per
environment, with how much each differs from production (*76 MB +32 MB*): what promoting that version adds, and
**Image size** charts it per environment over the last 90 days, from the deploy history: each step is a deploy, so
a heavier base or a new dependency stands out. The metric `goliash_image_size_bytes` and the dashboards' *Image
size* graph show it in Grafana and SigNoz. Agents read it for private images from agent 1.61.

## Is it running?

**Security → Is it running?** answers "is CVE-2024-3094 in production?" in seconds, and lists every known
vulnerability of the running images:

![Known vulnerabilities, only those exploited in the wild: two Log4j CVEs marked exploited, ransomware and critical with CISA's due dates, in log4j-core 2.14.1 of payments-api in prod, fixed in 2.15.0 and 2.16.0](../../../assets/vulns.png)

1. For images whose registry holds an SBOM (a BuildKit or cosign attestation, or an OCI referrer, see
   [SBOM of what runs](../sbom/#where-the-packages-come-from)), Goliash reads its package list once: package URLs
   such as `pkg:deb/debian/openssl@3.0.11-1~deb12u2` or `pkg:npm/lodash@4.17.15`.
2. It asks [OSV](https://osv.dev) which known vulnerabilities affect those packages, again every day, and reads
   each advisory once for its CVE aliases, summary and severity. Debian, Ubuntu and Alpine packages are asked about
   in their distribution release, Wolfi and Chainguard packages under their source package (Chainguard's own
   advisories), language packages (npm, PyPI, Go, Maven, crates, RubyGems, NuGet …) by their package URL.
3. Search by CVE or advisory ID, package or service. A vulnerability shows once, under its CVE, with every advisory
   that names it (`DEBIAN-CVE-…`, `DSA-…`, `GHSA-…`) and each package and service it is in.

**What to patch first.** Vulnerabilities in CISA's catalog of
[known exploited vulnerabilities](https://www.cisa.gov/known-exploited-vulnerabilities-catalog) (KEV) come first,
marked *exploited* with CISA's due date and *ransomware* when CISA knows of ransomware using them; *Only exploited
in the wild* shows just those. Each affected package says the version that fixes it (the lowest above the running
one, from OSV), or *no fix yet*. The catalog is read once a day; `GOLIASH_KEV_URL` names a mirror or turns it off.

![The Security panel of a service page: four running images, each signed with an SBOM and provenance, and its vulnerabilities, the two exploited Log4j CVEs first with the versions that fix them, then jackson-databind ones by severity](../../../assets/service-security.png)

Each service's page has a **Security** panel too: its running images per environment, signed or not, with an SBOM
and provenance or not, and the vulnerabilities in them, exploited ones first, each with the package and the version
that fixes it.

A cell of the matrix whose running image has a vulnerability exploited in the wild shows it as a red
*exploited CVE-…* badge (its tile needs attention), and [Updates](../versions/#updates) puts that upgrade first.

A notification rule with the `vulnerability` event announces each exploited or critical vulnerability as it starts
running ([Notifications](../notifications/#vulnerabilities)).

The answer covers only images with an SBOM Goliash could read, and the page says how many those are. While
advisories are still being looked up it says so, and does not answer "not running".

Only package names and versions are sent to OSV, never image, service or host names. `GOLIASH_OSV_URL` points the
lookups at a mirror, or turns them off (`off`).

## Trend

Goliash records production's posture every hour (the day's last reading stays) and keeps a little over a year of
it. Under the tiles, **Trend** draws the last 90 days of the median exposure, how many services run behind a newer
release, how many for 90+ days and how many past end of life, each with where it started: green when it went down.
The [weekly report](../notifications/#security-posture-report) adds the numbers from a week before, and
`GET /api/v1/posture` returns them once a week as `history`, so [Goli](../goli/) can answer "is patching getting
faster?".

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
- **Export SBOM** gives a CycloneDX 1.6 bill of materials of everything that runs, with the packages of each
  image's SBOM nested inside: for Dependency-Track, a vendor questionnaire or the software inventory NIS2 and the
  Cyber Resilience Act ask for.
- **Dependency-Track**: Integrations → Dependency-Track gives a script for cron or a scheduled CI job that uploads
  one project per environment (`goliash prod`, version `running`) from
  `/api/v1/inventory?format=cyclonedx&environment=…`. Its API key needs `BOM_UPLOAD` and `PROJECT_CREATION_UPLOAD`;
  the default *Automation* team lacks the second, and the upload answers 401 without it.
- **Print or save as PDF** prints the page with the time it was generated, as evidence of patch management for
  frameworks that ask for it (NIS2, DORA, SOC 2, ISO 27001).

- **Weekly report**: a notification rule with the *security posture report* event sends these numbers and the
  longest exposures every Monday (or day) to Slack, Teams, e-mail or any channel. See
  [Notifications](../notifications/#security-posture-report).
- **Trends and alerts**: `/metrics` serves the posture (`goliash_exposure_median_days`, `goliash_exposure_p90_days`,
  `goliash_exposures`, `goliash_end_of_life`, `goliash_accepted_risk`, `goliash_exposure_days`), the Grafana and SigNoz
  dashboards chart it over time (is patching getting faster?), and the Prometheus rules alert on production past its
  end of life and on 90+ days behind. See [Grafana dashboard](../grafana/) and [SigNoz dashboard](../signoz/).

The same data is available from the REST API (`GET /api/v1/posture`, for GRC tools and risk registers; see the
[API reference](../../reference/api/)), to AI assistants over [MCP](../../reference/mcp/) (`posture`), and to
[Goli](../goli/): **Explain** on every exposure asks what the releases in between fix and what to do first.
