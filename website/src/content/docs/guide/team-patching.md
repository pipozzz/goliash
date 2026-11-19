---
title: 'How-to: patching by team'
description: 'Give services a team, set deadlines, tell each team about its own upgrades and see which teams patch slowest.'
---

A team lead wants three things from Goliash: to hear when one of the team's upgrades nears its deadline, a weekly
summary of how the team patches, and a place to see it next to the other teams. The pieces are in different guides;
this page puts them in order. Try it on example data first with `goliash try`, which has ten teams and three
weeks of upgrades; its deadlines are not set, so step 2 is yours.

## 1. Give services a team

A service's team is its **owner**. If your workloads carry a `team` or `owner` label (or `goliash.team`,
`app.kubernetes.io/team`), there is nothing to do: Goliash reads it. Otherwise set it by hand, which always wins:

- **Settings → Teams**: assign services to a team, or rename one;
- or from a terminal:

```sh
goliash service set -name postgres -owner team-data
```

Backstage and the deploy repository's `CODEOWNERS` are read too: see [Teams](/guide/apps/#teams) for the order.
Check the result on **Settings → Teams**: every team has a card with its services.

## 2. Set remediation deadlines

At the bottom of the **Security** page, an admin sets under **Remediation deadlines** how long each reason to
upgrade may wait: an exploited vulnerability, a security fix, and a release past its end of life (from 3 days to
half a year each). A common start is 7, 30 and 90 days. [When each clock starts](/guide/security/#remediation-deadlines) is in the security guide.

From then on **Updates** marks each upgrade *due in 3 days* or *overdue 5 days*, and [Today](/guide/today/) lists
what is past its deadline first.

## 3. Tell each team

On a team's card on **Settings → Teams**, pick one of the workspace's channels (the team's Slack channel, say)
and press **Notify the team**. It adds two rules filtered by the team:

- its deadlines and critical vulnerabilities in production, as they happen;
- its own week, every Monday at 08:00 UTC: how many services are behind, what is past its deadline, and its time to
  patch.

The same from a terminal, for a channel named `team-data`:

```sh
goliash notify create -channel team-data -events deadline,vulnerability -owners team-data -production -mode instant
goliash notify create -channel team-data -events posture_report -owners team-data -mode weekly
```

The rules are ordinary [notification rules](/guide/notifications/): change or delete them under Notifications.

## 4. Read how the teams patch

The **Security** page has two panels for it, described in
[Time to patch, and patching by team](/guide/security/#time-to-patch-and-patching-by-team):

- **Time to patch in prod**: for each upgrade deployed to production, the days from the first newer release to the
  deploy, as a median per month. *The upgrades as CSV* is the list behind the bars.
- **Patching by team**: per team, the services in production, how many run behind and for how long, how many
  upgrades are past their deadline, and the team's median time to patch over 90 days. The slowest come first, and
  each team links to its Updates.

The workspace's weekly [posture report](/guide/notifications/#security-posture-report) names the slowest teams,
and the [monthly report](/guide/security/#for-audits) lists every upgrade deployed with the pull request that brought it.

## 5. Put it on the team's dashboard

The same numbers are [metrics](/guide/observability/#metrics) with an `owner` label, and the
[Grafana dashboard](/guide/grafana/) has a *Patching by team* row built on them:

| Metric | What it says |
|---|---|
| `goliash_team_behind` | the team's production services behind a newer release |
| `goliash_team_behind_median_days` | the median days they have been behind |
| `goliash_team_past_deadline` | the team's upgrades past their deadline |
| `goliash_team_patch_median_days` | the team's median time to patch, over 90 days |

An alert rule that tells a team's on-call when anything of theirs is past its deadline for a day:

```yaml
groups:
  - name: goliash-teams
    rules:
      - alert: UpgradePastDeadline
        expr: goliash_team_past_deadline{owner="team-data"} > 0
        for: 1d
        labels:
          team: team-data
        annotations:
          summary: '{{ $value }} upgrades of team-data are past their remediation deadline'
```
