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

## Upgrade with a pull request

With `GOLIASH_GITOPS_TOKEN` set on the server, the panel offers **Open PR → 1.7.0** next to an application that runs
an older version than the newest release the service's [version policy](/guide/versions/#version-policy) accepts.
Goliash then, in the repository the application deploys from (GitHub):

1. finds the YAML files under the application's path that pin the image at the running version: an image reference
   (`image: ghcr.io/acme/checkout:1.4.2`), Kustomize's `images` (`name` with `newTag`) or Helm values (`repository`
   with `tag`); other images, and the same tag of other images, stay as they are;
2. changes the tag on a new branch `goliash/<service>-<version>-<environment>`, from the branch the application
   follows;
3. opens a pull request saying what, where and why, with the release notes when known.

The **Delivery** page does the same for promotions: next to a version that runs in staging and waits for prod, **Open
PR → prod** pins that version in the repository of prod's application (one button per cluster when several run it),
saying since when it has run in staging.

Review and merge it as any change; Argo CD deploys it. Goliash never merges, never pushes to the branch an
application follows, and offers only the release the policy accepts. An image pinned by digest is left for you
(the new digest has to come with it).

The token is a [fine-grained GitHub token](https://github.com/settings/personal-access-tokens) limited to the deploy
repositories, with *Contents* and *Pull requests* read and write. For GitHub Enterprise set `GOLIASH_GITOPS_GITHUB_API`
to its API address (`https://github.example.com/api/v3`). Only members and admins see the button; each pull request is
in the audit log.

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
