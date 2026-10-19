---
title: Argo CD and Flux
description: 'Which Git repository, path and commit deploy each service, whether Argo CD or Flux is in sync, upgrades and promotions as pull requests, and links straight to the application, the repository and the commit.'
---

With Argo CD, Git says what should run and Argo CD makes it so. Goliash shows both sides on one page: what Git
declares for a service, which commit was last synced, whether Argo CD is in sync and healthy, and what actually runs,
on which version, against the newest release upstream.

## What you see

- **The service page** has an *Argo CD* panel: per environment and cluster, the application that deploys the
  service, its repository and path (or Helm chart) with the branch or tag it follows, the commit last synced (a link to
  the commit on GitHub, GitLab, Bitbucket or Gitea), and its sync and health status, with how long it has been so.
  With a token for the repository (see [Set up](#set-up)), an application out of sync also says **which version Git
  pins** and which runs: *Git pins 1.7.0, runs 1.6.0*, the answer to "why did my change not go out?". It is read
  from the files under the application's path, at most every 10 minutes, never while you wait. The matrix cell's
  tooltip and the out-of-sync alert say the same.
- **The matrix and the tiles** mark a cell *Argo: out of sync*, *Argo: degraded* or *Argo: missing* when the
  application has been so for more than 10 minutes: Argo CD is out of sync for a moment on every change, and
  progressing while it rolls out, which is not news. On the tiles such a cell needs attention.

A notification rule with the `gitops` event (*Argo CD out of sync or degraded*) says so in Slack, Teams, e-mail or a
push, once per episode, with the commit last synced; Goli and AI assistants read the same through the `gitops` tool
and `GET /api/v1/gitops`, which for a degraded application also gives the version change just before it
(`after_change`). `GET /api/v1/pull-requests` and the `pull_requests` tool list the pull requests Goliash opened.

## Upgrade with a pull request

With `GOLIASH_GITOPS_TOKEN` set on the server, the panel offers **Open PR → 1.7.0** next to an application that runs
an older version than the newest release the service's [version policy](/guide/versions/#version-policy) accepts.
Goliash then, in the repository the application deploys from (GitHub, GitHub Enterprise or GitLab):

1. finds the YAML files under the application's path that pin the image at the running version: an image reference
   (`image: ghcr.io/acme/checkout:1.4.2`), Kustomize's `images` (`name` with `newTag`) or Helm values (`repository`
   with `tag`); other images, and the same tag of other images, stay as they are;
2. changes the tag on a new branch `goliash/<service>-<version>-<environment>`, from the branch the application
   follows;
3. opens a pull request (a merge request on GitLab, in one commit) saying what, where and why, with the release
   notes when known.

The **Updates** page offers **Open PR** on the same lines, next to *Put off*, and the **Security** page next to
each service that runs behind a newer release, so the person who finds the exposure can propose the fix. The **Delivery** page does the same for
promotions: next to a version that runs in staging and waits for prod, **Open
PR → prod** pins that version in the repository of prod's application (one button per cluster when several run it),
saying since when it has run in staging.

![A pull request preview: the repository github.com/acme/deploy, the branch goliash/checkout-2.4.0-prod from main, the title, the kustomization.yaml change from newTag 2.3.1 to 2.4.0, and the description](../../../assets/gitops-preview.png)

**Open PR** first shows a **preview**: the files that pin the image with the lines that change, the branch, the
title and the description, read from the repository and changing nothing; **Open the pull request** sends it. The
public demo shows the preview with made-up repositories and opens nothing.

With [Goli](/guide/goli/) on, the description also says **what changes**: Goli reads the release notes between the
two versions and lists, in a few lines, what breaks or needs a migration first, then the notable fixes, security
first. It is marked as Goli's, and asked once a day per upgrade.

Each pull request lists the known vulnerabilities the upgrade fixes (exploited in the wild first) and those it
brings, from the SBOMs of both images. A version is proposed once: after that the page links the pull request, marked *merged* once it is. The **Delivery** page lists the pull
requests of the last 30 days, open, merged or closed, who opened them, and *running* once the version runs, with how long after opening it began to (*3h after
opening*) and the median over them all at the top: your time from proposal to production. Goliash
asks the forge every 30 minutes; one closed without merging is offered again on the page (the version policy does not
open it again: closing it was a no).

### Roll back

When an application turns **Degraded** or **Missing** (for more than ten minutes) within a week of its version
replacing another on that cluster, the panel offers **Rollback PR → 1.6.2**: a pull request that pins the version
before again. Hover it to see why. The pull request says it rolls back, what went wrong and since when, and which
known vulnerabilities the older version brings back. As with every pull request, you review and merge it, and
GitOps deploys it.

![The Argo CD panel of keycloak: identity-staging is Synced but Degraded since 48 minutes, with Open PR → 26.0.0 and a red Rollback PR → 25.0.1](../../../assets/gitops-rollback.png)

The alert for a degraded or missing application says the same: *degraded on k8s-prod, an hour after 1.7.0
replaced 1.6.2*, the likeliest cause first.

### Automatic pull requests

A service's version policy can ask for them per environment: **Automatic pull requests** `dev=minor, staging=patch`
on its page (`"auto_pr": {"dev": "minor", "staging": "patch"}` in the policy) has Goliash open, every 30 minutes,
the pull request for dev when the newest acceptable release is at most a minor jump away, and for staging when it is
a patch. Production stays a decision, a click away. They are signed *auto-pr* in the pull request and the audit log;
one that cannot be opened (no file pins the tag) is tried again the next day.

Review and merge it as any change; Argo CD deploys it. Goliash never merges, never pushes to the branch an
application follows, and offers only the release the policy accepts. An image pinned by digest is left for you
(the new digest has to come with it).

The token is a [fine-grained GitHub token](https://github.com/settings/personal-access-tokens) limited to the deploy
repositories, with *Contents* and *Pull requests* read and write. For GitHub Enterprise set `GOLIASH_GITOPS_GITHUB_API`
to its API address (`https://github.example.com/api/v3`). For GitLab set `GOLIASH_GITOPS_GITLAB_TOKEN`: a project or
group access token with the `api` scope and the Developer role, on gitlab.com or the instance in `GOLIASH_GITLAB_URL`. Only members and admins see the button; each pull request is
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

## Flux

With Flux the same panel, badges (*Flux: out of sync*, *Flux: degraded*), alerts and pull requests work from Flux's
own objects; nothing to configure in Flux either. Flux labels everything it applies with its Kustomization or
HelmRelease (`kustomize.toolkit.fluxcd.io/name`, `helm.toolkit.fluxcd.io/name`), and Goliash reads those with their
sources:

- a **Kustomization**: its GitRepository's URL and branch (or tag, semver), its path, and the commit last applied
  (`main@sha1:…`); *out of sync* when the GitRepository has a newer commit than the one applied, *degraded* when its
  `Ready` condition fails (reconciliation, health checks), *suspended* when suspended;
- a **HelmRelease**: its chart, the version it follows and the one applied, and its HelmRepository; a HelmRelease
  wins over the Kustomization that applied it.

`ssh://git@github.com/acme/deploy` and `git@…` addresses link to the repository's web page, and pull requests work
for them as for Argo CD. The Helm charts grant `get` and `list` on Flux's `kustomizations`, `helmreleases`,
`gitrepositories` and `helmrepositories`.

## Why it helps

- **Drift with a cause.** A service behind upstream or behind the previous environment, and the commit and path that
  pin it: one click to the line to change.
- **Out of sync for an hour** is a deploy that did not happen: a sync window, a failing hook, auto-sync off. Goliash
  shows it next to the version that still runs.
- **Hand changes** stand out: *OutOfSync* while the running version differs from what Git declares.
