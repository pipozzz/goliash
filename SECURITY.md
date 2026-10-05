# Security policy

## Reporting a vulnerability

Please report vulnerabilities privately through GitHub:
**[Report a vulnerability](https://github.com/pipozzz/goliash/security/advisories/new)** (Security tab → Report a
vulnerability). Do not open a public issue.

Include what is affected (server, agent, Helm chart, …), the version, and how to reproduce it. You will get an answer
within a few days. Once a fix is released, the advisory is published with credit to you, unless you prefer
otherwise.

## Supported versions

| Version | Security fixes |
| --- | --- |
| 1.x, latest minor release | yes |
| older | upgrade to the latest release |

## Verifying releases

Images, Helm charts and release archives are signed with cosign (keyless, GitHub Actions) and come with SBOMs; see
[Verify a release](https://goliash.dev/security/#verify-a-release).
