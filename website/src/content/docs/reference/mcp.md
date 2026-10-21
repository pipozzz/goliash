---
title: AI assistants (MCP)
description: 'Ask Claude, Cursor and other AI assistants what runs where through the Goliash MCP server.'
---

Goliash speaks the [Model Context Protocol](https://modelcontextprotocol.io), so an AI assistant such as Claude or
Cursor can answer "which version of payments runs in prod, and is it behind upstream?", "what changed in prod in the
last two hours?", "what reaches end of life soon?" or "is Log4Shell running anywhere?" from live data.

Every tool calls the REST API with an API token, so the assistant sees and may do exactly what the token allows.
Create one for it; a viewer token only reads, a member token may also acknowledge:

```sh
goliash token create -name assistant -expires 90d            # reads
goliash token create -name assistant -role member -expires 90d  # also acknowledges
```

## Tools

| Tool | What it answers |
| --- | --- |
| `search` | Search everything at once: services by name, image, version, workload or GitOps application; vulnerabilities; packages; pull requests; history; notifications. The first tool for "where does openssl run?" |
| `matrix` | What runs where, filtered by service or environment, now or at a past time |
| `service` | One service: versions per environment, newest upstream release, open drift |
| `drifts` | What needs attention: env, upstream, inconsistent, declared, eol |
| `updates` | What to upgrade, most urgent first: running → target version, release notes and why; filter by owner, environment or urgency |
| `release_notes` | What changed between two versions of a service: the release notes in between, from its GitHub or GitLab releases |
| `changes` | What changed: recently (default: last 24 hours), or in a window of the past (`since` and `before` as times) |
| `promotions` | What waits for the next environment, with the releases it brings, the vulnerabilities it fixes and brings, and the packages it brings under demanding licenses |
| `rollouts` | Rollouts that do not finish: workloads down, stuck with two versions side by side, or short of replicas |
| `gitops` | Is Git what runs? Argo CD applications and Flux Kustomizations: repository, path, commit, sync and health; for a degraded one, the version change just before it and so the version to roll back to; sync windows and manual sync |
| `pull_requests` | Pull requests Goliash opened to upgrade, promote or roll back: open, merged or closed, by whom, their checks, whether the version runs now and how long after merging it began to; for a merged one not deployed, why it waits |
| `delivery` | Deploys per environment and lead times, last 30 days |
| `inventory` | Every running container with image and digest, now or at a past time, with image size and whether its base is current |
| `images` | Why did an image grow? One service's images: size against its budget, base and how far behind it is, evidence, 90 days of sizes, and the layers the last deploy added and removed with the Dockerfile step of each |
| `hygiene` | Moving tags, tags pushed again, untrusted registries, images built on an outdated base |
| `licenses` | Licenses of the packages in running images, most demanding first (AGPL, BUSL, GPL, unknown…), with where each runs |
| `vulnerabilities` | Is a CVE running? Known vulnerabilities of running images, exploited ones first, with the services and the version that fixes each |
| `posture` | The security posture: exposure days (median, p90, 30+, 90+), end of life, accepted risk, blind spots (with why each service's newest release is unknown) and every exposure |
| `notifications` | What Goliash sent: alerts, digests and upgrade plans, each once, newest first (default: 7 days) |
| `status` | Whether the workspace is in a change freeze (`change_freeze`), and Goliash's own health: agents reporting, releases checked, vulnerability data current, notifications going out; whether an answer can be trusted |
| `acknowledge` | Quiet notifications until a version or a time (needs a member token; the only tool that writes) |

## Remote (HTTP)

Every Goliash server serves MCP at `/mcp` (Streamable HTTP). Apps sign in with **OAuth**, the way the MCP
specification describes, so no token is copied anywhere:

```sh
claude mcp add --transport http goliash https://goliash.example.com/mcp
```

The first time, `/mcp` → **Authenticate** in Claude Code (Claude Desktop, Cursor and VS Code do the same) opens
Goliash in the browser: sign in as usual, pick the workspace and **Allow**. The app then reads Goliash as you:

- **Read only**, whatever your role; tools that change something (`acknowledge`) are refused.
- What you can see in that workspace, and only while you are a member of it.
- Access tokens last an hour and the app renews them; after 30 days without use it has to ask again.
- **Your account → Apps with access** lists every app with the workspace and when it last read, and **Revoke** ends
  it at once.
- Admins see every app in the workspace, and whose it is, under **Users → Apps with access**, and can revoke any.

For Cursor, in `.cursor/mcp.json`, the URL is enough:

```json
{ "mcpServers": { "goliash": { "url": "https://goliash.example.com/mcp" } } }
```

### Try it on the demo

The [live demo](https://demo.goliash.dev) takes AI assistants too, with no account:

```sh
claude mcp add --transport http goliash-demo https://demo.goliash.dev/mcp
```

Then `/mcp` → **Authenticate**, **Allow** on the page that opens, and ask: *"What should we upgrade first in
prod, and why?"*, *"What changed in staging this week?"*, *"Which services run past their end of life?"* The
assistant reads the demo's example data as its read-only viewer.

### With an API token

Scripts, CI jobs and apps without OAuth send a viewer (or member) API token instead; **Integrations → AI
assistants** makes one and fills in the command:

```sh
claude mcp add --transport http goliash https://goliash.example.com/mcp \
  --header "Authorization: Bearer glsh_api_…"
```

### How the sign-in works

Goliash is both the MCP server and its OAuth 2.1 authorization server:

| | |
| --- | --- |
| Discovery | `/mcp` answers 401 with `resource_metadata`; `/.well-known/oauth-protected-resource` (RFC 9728) names the server, `/.well-known/oauth-authorization-server` (RFC 8414) its endpoints |
| App identity | Client ID Metadata Documents (the app's `client_id` is an https URL Goliash fetches, from the public internet only), or Dynamic Client Registration (RFC 7591) for older apps |
| Sign-in | `/oauth/authorize`: your usual sign-in (password, passkey, single sign-on, two-factor), then a consent page; authorization codes with PKCE (S256) only, ten minutes, single use; `iss` in the answer (RFC 9207) |
| Tokens | `/oauth/token`: bound to `https://…/mcp` (`resource`, RFC 8707), access one hour, refresh 30 days and rotated on every use; `/oauth/revoke` (RFC 7009) |
| Redirects | https, or http on a loopback address (any port, RFC 8252); anything else is refused, and an unregistered one is never followed |

`GOLIASH_PUBLIC_URL` should be set: it is the issuer apps check, and the address in every link.

## Local (stdio)

`goliash mcp` runs an MCP server on stdin and stdout that talks to your Goliash server, for apps that only start
local MCP servers. After `goliash login https://goliash.example.com` it needs nothing else (`"args": ["mcp"]`); with
an API token instead, for Claude Desktop, in `claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "goliash": {
      "command": "goliash",
      "args": ["mcp"],
      "env": { "GOLIASH_URL": "https://goliash.example.com", "GOLIASH_TOKEN": "glsh_api_…" }
    }
  }
}
```
