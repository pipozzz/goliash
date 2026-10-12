---
title: SBOM of what runs
description: "A CycloneDX 1.6 software bill of materials of everything that actually runs, per environment and at any point in time, with each image's packages, for Dependency-Track, auditors and the Cyber Resilience Act."
---

A **software bill of materials** (SBOM) lists what a piece of software is made of: every component with its exact
version, a package URL that identifies it (`pkg:deb/debian/openssl@3.0.15-1~deb12u1`) and a hash. Security tools
match it against vulnerability feeds; auditors and customers ask for it; the EU Cyber Resilience Act, NIS2 and the
US Executive Order 14028 make it the expected answer to "what software do you run?".

Most SBOMs describe **one image at build time**. Goliash gives you the SBOM of **what actually runs**: every image in
every environment, read from the runtime, with the packages of each image nested inside, for now or for any moment
of the last 400 days.

## What is in it

One [CycloneDX 1.6](https://cyclonedx.org/) JSON document, validated against the official schema:

- **One component per running image**, type `container`, with its OCI package URL and SHA-256 digest, so the
  component is the exact build that runs and not a tag that may have moved.
- **Where it runs**, as properties: `goliash:environment`, `goliash:target`, `goliash:platform`, `goliash:service`
  and `goliash:workload`. An image that runs in three clusters lists all three.
- **Its packages**, nested inside the image, when its registry holds an SBOM attestation (see
  [Where the packages come from](#where-the-packages-come-from)): Debian, Alpine, Wolfi and RPM packages, npm, PyPI, Maven,
  Go modules and whatever else the build's SBOM lists, each with its package URL.
- **Servers watched by the [host collector](../../reference/collectors/#linux-servers)**: the operating system as an
  `operating-system` component and the packages Goliash follows as `application` components.
- **Software added by hand** (a managed database, an appliance) as `application` components.

```json
{
  "bomFormat": "CycloneDX",
  "specVersion": "1.6",
  "metadata": {
    "timestamp": "2026-10-12T09:30:00Z",
    "tools": { "components": [{ "type": "application", "name": "goliash", "version": "1.31.0" }] },
    "component": { "type": "application", "name": "Default: running software" }
  },
  "components": [
    {
      "type": "container",
      "name": "ghcr.io/acme/payments-api",
      "version": "2.14.1",
      "purl": "pkg:oci/payments-api@sha256%3A9f2c…?repository_url=ghcr.io%2Facme%2Fpayments-api&tag=2.14.1",
      "hashes": [{ "alg": "SHA-256", "content": "9f2c…" }],
      "properties": [
        { "name": "goliash:environment", "value": "prod" },
        { "name": "goliash:target", "value": "eks-prod-eu" },
        { "name": "goliash:service", "value": "payments-api" },
        { "name": "goliash:workload", "value": "payments/payments-api" }
      ],
      "components": [
        { "type": "library", "name": "debian/openssl", "version": "3.0.15-1~deb12u1", "purl": "pkg:deb/debian/openssl@3.0.15-1~deb12u1?arch=amd64" },
        { "type": "library", "name": "org.apache.logging.log4j/log4j-core", "version": "2.14.1", "purl": "pkg:maven/org.apache.logging.log4j/log4j-core@2.14.1" }
      ]
    }
  ]
}
```

## Export it

| Where | What you get |
| --- | --- |
| **SBOM** above the matrix | Everything that runs now, or at the moment [time travel](../matrix/#looking-back-and-exporting-for-audits) shows |
| **Security → Export SBOM** | Everything that runs now |
| `GET /api/v1/inventory?format=cyclonedx` | The same over the [REST API](../../reference/api/); add `environment=prod` for one environment and `at=2026-09-12T14:00` for a moment in the past |
| `goliash inventory -format cyclonedx` | The same from the CLI, with `-environment` and `-at` |

```sh
# what ran in production on 12 September at 14:00, for the auditor
curl -H "Authorization: Bearer $GOLIASH_TOKEN" \
  "https://goliash.example.com/api/v1/inventory?format=cyclonedx&environment=prod&at=2026-09-12T14:00" \
  > prod-2026-09-12.cdx.json
```

A viewer API token is enough: it reads only.

## Dependency-Track

[Dependency-Track](https://dependencytrack.org/) watches SBOMs against its vulnerability feeds and your policies.
**Settings → Integrations → Dependency-Track** creates a viewer token and gives a script that uploads one project
per environment, `goliash dev`, `goliash staging`, `goliash prod`, each with version `running`, creating them on the
first upload. Run it every hour from cron or a scheduled CI job and Dependency-Track always tracks what runs, not
what was built.

The Dependency-Track API key needs `BOM_UPLOAD` and `PROJECT_CREATION_UPLOAD`. The default *Automation* team has the
first but not the second; without it the upload answers 401.

## Where the packages come from

Goliash does not scan images. It reads the SBOM the build already attached to the image in its registry, SPDX or
CycloneDX, wherever it is:

- **BuildKit**: `docker buildx build --sbom=true`, or `sbom: true` in GitHub Actions' `docker/build-push-action`.
  That one flag is all an image needs.
- **cosign**: `cosign attest --type cyclonedx` or `--type spdxjson`, for example with an SBOM from
  [Syft](https://github.com/anchore/syft) or Trivy. Chainguard images come with one per platform; Goliash reads the
  linux/amd64 one when the image index's own SBOM only lists its platforms.
- **OCI referrers**: an SBOM pushed as an artifact (`oras attach --artifact-type application/vnd.cyclonedx+json`) or
  a Sigstore bundle with an SBOM predicate.

Images on public registries are read by the server. Images on private registries are read by the agents with the
credentials they already use, or by the server with read access given under
[Settings → Private registries](../../reference/collectors/#credentials-on-the-server). Each image is read once; its
package list is kept with its digest.

**Security** shows how many production images carry an SBOM, a signature and provenance, and lists those without.
An image without an SBOM still appears in the export, with its digest and where it runs, just without packages
inside: add `--sbom=true` to its build and the next export has them.

## Vulnerabilities from the same packages

The packages behind the export also answer **Security → Is it running?**: Goliash asks
[OSV](https://osv.dev) daily which known vulnerabilities affect them, puts those CISA knows to be exploited first, and
names the version that fixes each. See [Is it running?](../security/#is-it-running).

## For audits and regulation

- **Cyber Resilience Act and NIS2**: an up-to-date inventory of the software you operate, per environment, in a
  standard format, with history: what ran when an incident happened.
- **SOC 2 and ISO 27001**: asset inventory and change evidence; pair the SBOM with the CSV and PDF of the Security
  page.
- **Customer and vendor questionnaires**: hand over the SBOM of production instead of filling in component lists.

The export only reads what Goliash already knows; it sends nothing anywhere. Package names and versions go to OSV
for the vulnerability lookups only, never image, service or host names.
