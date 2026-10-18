---
title: Argo CD
description: 'Which Git repository, path and commit deploy each service, whether Argo CD is in sync, and links straight to the application, the repository and the commit.'
---

With Argo CD, Git says what should run and Argo CD makes it so. Goliash shows both sides on one page: what Git
declares for a service, which commit was last synced, whether Argo CD is in sync and healthy, and what actually runs,
on which version, against the newest release upstream.

## What you see

- **The service page** has an *Argo CD* panel: per environment and cluster, the application that deploys the
  service, its repository and path (or Helm chart) with the branch or tag it follows, the commit last synced (a link to
  the commit on GitHub, GitLab, Bitbucket or Gitea), and its sync and health status, with how long it has been so.
- **The matrix and the tiles** mark a cell *Argo: out of sync*, *Argo: degraded* or *Argo: missing* when the
  application has been so for more than 10 minutes: Argo CD is out of sync for a moment on every change, and
  progressing while it rolls out, which is not news. On the tiles such a cell needs attention.

A notification rule with the `gitops` event (*Argo CD out of sync or degraded*) says so in Slack, Teams, e-mail or a
push, once per episode, with the commit last synced; Goli and AI assistants read the same through the `gitops` tool
and `GET /api/v1/gitops`.

## Set up

Nothing to configure in Argo CD. The agent (or the server collecting its own cluster) reads Argo CD's
`Application` objects, read-only, and finds each workload among the resources an application lists. The Helm charts
grant `get` and `list` on `applications.argoproj.io`; with your own RBAC add:

```yaml
- apiGroups: ["argoproj.io"]
  resources: ["applications"]
  verbs: ["get", "list"]
```

Applications in any namespace are found (Argo CD's *apps in any namespace* included), and multi-source applications
show the source with a path or chart. Without Argo CD in the cluster, or without the permission, nothing changes.

To link application names to the Argo CD UI, set its address on the server:

```sh
GOLIASH_ARGOCD_URL=https://argocd.example.com
```

## Why it helps

- **Drift with a cause.** A service behind upstream or behind the previous environment, and the commit and path that
  pin it: one click to the line to change.
- **Out of sync for an hour** is a deploy that did not happen: a sync window, a failing hook, auto-sync off. Goliash
  shows it next to the version that still runs.
- **Hand changes** stand out: *OutOfSync* while the running version differs from what Git declares.
