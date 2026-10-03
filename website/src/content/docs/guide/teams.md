---
title: Teams and workspaces
---

## Signing in

- **Magic links.** `goliash login-link -email you@example.com` prints a one-time link. The first person becomes the
  owner. With SMTP configured, people request links by e-mail on the sign-in page.
- **OIDC.** Any OpenID Connect provider (Google, Microsoft Entra ID, Okta, Keycloak, Authentik, …). Set
  `GOLIASH_OIDC_ISSUER`, `GOLIASH_OIDC_CLIENT_ID` and `GOLIASH_OIDC_CLIENT_SECRET`, and register the redirect URI
  `<public URL>/auth/oidc/callback`. People from the e-mail domains in `GOLIASH_OIDC_DOMAINS` get an account as
  viewers on first sign-in; others need an invitation.

## Roles

| Role | Can |
| --- | --- |
| viewer | read everything in the workspace |
| member | also map services, edit version policies and acknowledge |
| admin | also manage agents, targets, channels, rules, API tokens and people |
| owner | everything, in every workspace |

Viewer, member and admin are granted **per workspace**. Admin and owner can also be granted for the whole
**organization**: they then reach every workspace.

Organization admins change roles on the Users page. Only owners grant or take the owner role, the last owner cannot
be removed, and nobody changes their own role. Every change is recorded in the audit log.

## Workspaces

A workspace has its own agents, targets, services, history, notifications and API tokens. Use one per client (for
an MSP) or per team.

```sh
goliash workspace create -name "Client A" -slug client-a -envs     # -envs adds dev, staging and prod
goliash user create -email ops@client-a.example -role member -workspace client-a
goliash user grant -email ops@client-a.example -role admin -workspace client-a
goliash matrix -workspace client-a
```

Organization owners and admins switch workspaces in the top bar. Everyone else sees only the workspaces they were
invited to, and a workspace admin manages the people of that workspace only, so clients never see each other.

Every CLI command takes `-workspace` (or `GOLIASH_WORKSPACE`); without it, it works on the first workspace.

## API tokens

API tokens (`glsh_api_…`) belong to one workspace and act as a member: they read everything and can acknowledge.
Create them on the Users page or with `goliash token create -name prometheus`. The token is shown once.

## Audit log

Every change to configuration, tokens and roles, from the UI, the API or the CLI, and every sign-in is recorded.
Admins see the log on the Users page. Secrets never appear in it.
