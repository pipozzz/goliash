---
title: Pricing
description: 'Goliash is free and open source: every feature, every environment, no seats and no node limits. What the AGPL-3.0 and Apache-2.0 licences mean when you run it, and what to do if your policy blocks the AGPL.'
---

Goliash is free. All of it.

There is no paid tier, no trial that expires, no per-seat or per-node count, and no feature that opens a
"contact us" dialog instead of a page. What you self-host is the whole product.

| | |
| --- | --- |
| Price | Free |
| Features | All of them |
| Environments, clusters, targets | As many as you run |
| Users and teams | As many as you have |
| History | The full 400-day timeline, on your own database |
| Hosted service | None. You run it |

## Why there is no price

A tool that answers "what runs where, on which version" is only worth anything when it sees *everything* — every
environment, every cluster, the forgotten Compose box included. Priced per node or per seat, it gets deployed to the
important half of the estate, and the blind spots it was bought to remove survive in the cheap half.

So it is licensed, not sold. The code that is published is published under the licences below, and a licence already
granted cannot be withdrawn from the releases that carry it.

## The licences

Goliash is split in two, and the split is deliberate:

| Component | Licence | Why |
| --- | --- | --- |
| `goliash-agent`, and everything it is built from | [Apache-2.0](https://github.com/pipozzz/goliash/blob/main/LICENSES/Apache-2.0.txt) | The agent runs inside your infrastructure, so it must be auditable and free of licensing concerns |
| `goliash` server, UI, API, migrations, deploy manifests | [AGPL-3.0-only](https://github.com/pipozzz/goliash/blob/main/LICENSES/AGPL-3.0-only.txt) | Anyone who offers the server *as a service* publishes their changes |

What that means in practice for almost everyone:

- **Running Goliash inside your own company — including commercially, including on customer workloads you operate —
  costs nothing and obliges nothing.** Use it, modify it, keep the changes to yourself.
- The AGPL's source-sharing obligation is triggered by offering **the Goliash server itself** to third parties as a
  service, not by using it to watch the services you run.
- The agent sits under Apache-2.0 on purpose, because it is the part that runs next to your workloads. CI enforces
  the boundary: the agent binary must never pull in AGPL code.

The full rules, including which paths carry which SPDX header, are in
[`LICENSING.md`](https://github.com/pipozzz/goliash/blob/main/LICENSING.md).

## If your policy blocks the AGPL

Some organisations block AGPL-licensed software by policy, whatever the use. If that is the blocker between you and
running Goliash, say so in a [GitHub Discussion](https://github.com/pipozzz/goliash/discussions) — it is worth
talking about rather than letting a licence header decide it.

## Support

Support is the community kind: [Discussions](https://github.com/pipozzz/goliash/discussions) for questions and
[issues](https://github.com/pipozzz/goliash/issues) for bugs and requests. There is no paid support plan today, and
no answer-within-an-hour promise to go with it.

Security problems go through the private channel instead:
[report a vulnerability](https://github.com/pipozzz/goliash/security/advisories/new). Releases are signed with
cosign and ship SBOMs — [verify a release](/security/#verify-a-release).

## If you want to help

The project costs you nothing, so the useful currencies are these:

- **Tell it what it got wrong.** A bug report that names the orchestrator, the image and what Goliash showed instead
  is worth more than a feature request.
- **Star the [repository](https://github.com/pipozzz/goliash)** — it is how the next person finds it.
- **Write about what you found.** "We pointed it at three clusters and discovered X" is the review nobody can write
  for you.
- **Contribute.** The agent is Apache-2.0 and the collectors are the easiest place to start.
