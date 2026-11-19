---
title: Today
description: 'The page Goliash opens on: what to do first, in one list, from what the other pages know.'
---

**Today** is where a sign-in lands: one short list of what to do first, so you do not have to walk through Updates,
Delivery and Security to find it.

| Part | What is on it |
|---|---|
| **Rollouts that do not finish** | What is broken now: a rollout stuck half way, a service running none of its replicas |
| **Upgrade first** | Upgrades that cannot wait, most urgent first and production first: a vulnerability exploited in the wild (CISA KEV), a release that fixes vulnerabilities, a release cycle past its end of life, and anything past your [remediation deadline](/guide/security/). At most eight; the rest are a click away |
| **Since yesterday** | What changed: new security releases for what runs, vulnerabilities newly listed as exploited, fixes merged and not deployed |

Each upgrade has the same actions as on Updates: **Open PR** when Argo CD, Flux or `goliash.git.*` labels say where
its version is pinned, **Put off** with a reason, **Brief** from Goli. Majors, minors and patches that can wait are
only counted, with a link to [Updates](/guide/versions/).

When nothing has to be done, the page says so — that is the state to aim for.

If you saved a view as your home page, signing in still opens that view; Today stays first in the navigation.
