---
title: Teams and workspaces
description: 'Sign in with passwords, links or OIDC, roles per workspace, API tokens and sessions, and one workspace per team or client.'
---

## Signing in

- **Passwords.** Everyone may set a password on their account page (the avatar in the top bar → Your account), or an admin
  runs `goliash user password -email you@example.com`. Forgot it? Sign in with a link and set a new one: for 15
  minutes after a link or single sign-on, the current password is not asked for.
- **Magic links.** `goliash login-link -email you@example.com` prints a one-time link. The first person becomes the
  owner. With SMTP configured, people request links by e-mail on the sign-in page.
- **OIDC.** Any OpenID Connect provider (Google, Microsoft Entra ID, Okta, Keycloak, Authentik, …). Set
  `GOLIASH_OIDC_ISSUER`, `GOLIASH_OIDC_CLIENT_ID` and `GOLIASH_OIDC_CLIENT_SECRET`, and register the redirect URI
  `<public URL>/auth/oidc/callback`. People from the e-mail domains in `GOLIASH_OIDC_DOMAINS` get an account as
  viewers on first sign-in; others need an invitation.

## Locked out?

The images have no shell, but the `goliash` binary in them prints a sign-in link or sets a password. Run it next to
the server, on the same database:

```sh
docker exec goliash goliash login-link -email you@example.com          # Docker: one-time sign-in link
echo 'a long new password' | docker exec -i goliash goliash user password -email you@example.com
kubectl -n goliash exec deploy/goliash -- goliash login-link -email you@example.com
nomad alloc exec -task goliash <alloc-id> goliash login-link -email you@example.com
```

With an empty database (a container recreated without its volume) the first `login-link` creates you as the owner.

**No exec, or a console that wants a shell** (Nomploy, many PaaS): set `GOLIASH_RECOVERY_EMAIL=you@example.com` on
the server and restart it. Every start then logs a one-time sign-in link for you (the log line starts with
`recovery:`); open it within 15 minutes, set a password on your account page and remove the variable again. It
only works for someone who already has an account, or creates you as the owner when nobody has one yet. In the
Nomad pack the variable is `recovery_email`.
Keep `/data` on a volume: it holds the database and `goliash.key`.

## Sessions

The account page lists every browser you are signed in on, with its address and when it was last used; sign any of
them out, or all but this one. Changing your password signs out the others. On **Settings → Users and API tokens** admins sign a person
out everywhere or remove their password (when it leaked: they sign in with a link and choose a new one).

## Roles

| Role | Can |
| --- | --- |
| viewer | read everything in the workspace |
| member | also map services, edit version policies and acknowledge |
| admin | also manage agents, targets, channels, rules, API tokens and people |
| owner | everything, in every workspace |

Viewer, member and admin are granted **per workspace**. Admin and owner can also be granted for the whole
**organization**: they then reach every workspace.

Organization admins change roles on **Settings → Users and API tokens**. Only owners grant or take the owner role, the last owner cannot
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

API tokens (`glsh_api_…`) belong to one workspace. A **viewer** token (the default) reads everything; a **member**
token can also acknowledge. A token may expire after 30, 90 or 365 days, or never. Create them on **Settings → Users and API tokens** or
with `goliash token create -name prometheus [-role member] [-expires 90d]`; the token is shown once. **Settings → Users and API tokens**
lists every token with who created it and when it was last used, and revokes one at once (`goliash token revoke
-name prometheus`). Things done with a token show its name in the audit log and on acknowledgements.

## Audit log

Every change to configuration, tokens and roles, from the UI, the API or the CLI, and every sign-in is recorded.
Admins see the log on **Settings → Users and API tokens**. Secrets never appear in it.
