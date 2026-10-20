---
title: Change freezes
description: 'Periods without changes you do not have to make: automatic pull requests wait, those opened by hand say so, every page shows it. One-off or every week.'
---

A **change freeze** is a period when the workspace takes no change it does not have to: the holidays, a big sale, a
migration weekend, or every Friday afternoon. Goliash does not deploy anything itself, so a freeze is about the pull
requests it opens and about telling everyone.

## Add one

**Settings → Change freezes** (admins) takes a start and an end in UTC and why (*Christmas*, *the Black Friday
sale*). Tick **every week** for one that comes back: give its first start and end, less than a week apart, such as
Friday 15:00 to Monday 08:00, and it holds every week the same days and hours. The list shows each one (a weekly one
as *every Friday 15:00 → Monday 08:00 UTC*), marked *now* while it holds; **End it** ends one early, **Remove** drops
one that has not started. Adding and ending one is in the audit log.

## While one holds

- Every page says so at the top: *❄️ Change freeze until 3 Jan 08:00 UTC: Christmas* (TV mode excepted).
- The version policy's [automatic pull requests](/guide/argocd/#automatic-pull-requests) wait until it ends.
- A pull request opened by hand (an upgrade, a promotion, several at once, a whole application) still opens, for what
  cannot wait, but its preview warns first, and it starts with *❄️ Opened during a change freeze (until …)*, so the
  reviewer knows.
- A [rollback](/guide/argocd/#roll-back) is not held back nor warned about: undoing a change that broke something is
  what a freeze is for.
- `GET /api/v1/status` says `change_freeze` (`until`, `note`), so do the MCP `status` tool and Goli, who mentions it
  first when asked about upgrades.

A freeze changes nothing Argo CD or Flux do: a pull request merged during one still deploys. To stop deploys
themselves, use their own sync windows.
