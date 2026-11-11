---
title: Single sign-on
description: 'Sign in with Google, Microsoft Entra ID, Okta, Keycloak or Authentik, set up in Settings, with roles from the provider''s groups.'
---

Goliash signs people in with any OpenID Connect provider: Google Workspace, Microsoft Entra ID, Okta, Keycloak,
Authentik, JumpCloud, GitLab and others. The whole organization uses one provider. The provider's groups can decide
who may sign in and with which role in each workspace.

An owner sets it up under **Settings → Single sign-on**, with no restart. The server needs a secret key to store the
client secret encrypted; it makes one at its first start (`GOLIASH_SECRET_KEY`, see
[Configuration](/reference/configuration/)).

## 1. Register Goliash with the provider

Create a web application (OAuth 2.0 / OIDC client, *confidential*, authorization code flow) with the redirect URI the
page shows:

```
https://goliash.example.com/auth/oidc/callback
```

That is `GOLIASH_PUBLIC_URL` followed by `/auth/oidc/callback`, so the public URL must be the address people use.

| Provider | Where | Issuer URL |
| --- | --- | --- |
| Google Workspace | Google Cloud console → APIs & Services → Credentials → OAuth client ID → Web application | `https://accounts.google.com` |
| Microsoft Entra ID | Entra admin center → App registrations → New registration → Web; then Certificates & secrets → New client secret | `https://login.microsoftonline.com/<tenant ID>/v2.0` |
| Okta | Admin → Applications → Create App Integration → OIDC → Web Application | `https://<your domain>.okta.com` |
| Keycloak | Clients → Create client → OpenID Connect, client authentication on | `https://<host>/realms/<realm>` |
| Authentik | Applications → Providers → OAuth2/OpenID Provider (confidential) | `https://<host>/application/o/<slug>/` |

Copy the **client ID** and **client secret**.

## 2. Fill in Settings → Single sign-on

- **Issuer URL**: from the table above.
- **Button label**: what the sign-in page says, e.g. *Google*.
- **Client ID** and **client secret**: from step 1. Once saved, the secret is never shown again; leave the field
  empty to keep it.

**Save** first checks that the provider answers (its discovery document), so settings that do not work are refused
and the sign-in in place keeps working. The sign-in page then offers the provider's button.

## 3. Decide who gets in

Two ways, pick one:

- **Groups** (recommended). Add a row per group of the provider: the group, the role, and the workspace.
  - *admin*: an admin of the whole organization;
  - *member* or *viewer*: in every workspace, or in the one chosen.

  At every sign-in the provider's groups decide: someone in none of these groups cannot sign in, and roles follow
  group changes. Owners stay owners whatever their groups.
- **E-mail domains**, without groups: people with an address in these domains get a viewer account in the first
  workspace at their first sign-in. Everyone else needs an invitation.

Example:

| Group | Role | Workspace |
| --- | --- | --- |
| `goliash-admins` | admin | |
| `sre` | member | every workspace |
| `team-payments` | member | payments |
| `everyone` | viewer | every workspace |

### Getting the groups into the token

Goliash reads the groups from the ID token's `groups` claim. Change the claim under **More** when the provider uses
another name.

- **Entra ID**: App registration → Token configuration → Add groups claim (security groups). Entra sends group
  *object IDs*: use those as the group names, or turn on *cloud-only group display names*.
- **Okta**: in the app's Sign On tab, set a groups claim (filter, e.g. *Matches regex* `.*`).
- **Keycloak**: add a *Group Membership* mapper to the client (claim `groups`, full group path off).
- **Authentik** and **Keycloak**: add `groups` under **More → More scopes** when the provider sends groups only then.
- **Google Workspace**: Google sends no groups. Use e-mail domains, or SCIM below.

## 4. Test it

**Test a sign-in** runs the whole sign-in with the provider, but signs no one in and changes no account. It shows
the e-mail the provider confirmed, the groups it sent, and what signing in would give (*member in payments, viewer
in every workspace*) or why it would be refused. Test with an account in each group before you rely on it.

## SCIM provisioning

With SCIM the provider also creates, updates and deactivates accounts before anyone signs in, and pushes group
membership; the group rows above apply to it too. Turn it on with `GOLIASH_SCIM_TOKEN`, see
[Configuration](/reference/configuration/#scim-provisioning).

## Set up by environment variables instead

`GOLIASH_OIDC_ISSUER`, `GOLIASH_OIDC_CLIENT_ID`, `GOLIASH_OIDC_CLIENT_SECRET` and `GOLIASH_GROUP_ROLES` (see
[Configuration](/reference/configuration/#oidc-sign-in)) do the same for deployments kept in Git or Helm. When set,
they take precedence and the settings page shows them read-only.

## Turning it off

**Turn single sign-on off** on the same page. Accounts stay; people sign in with an e-mailed link, a passkey or a
password. Keep one owner who can sign in without the provider, so a provider outage does not lock everyone out.
