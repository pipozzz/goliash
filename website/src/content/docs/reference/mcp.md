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
| `matrix` | What runs where, filtered by service or environment, now or at a past time |
| `service` | One service: versions per environment, newest upstream release, open drift |
| `drifts` | What needs attention: env, upstream, inconsistent, declared, eol |
| `updates` | What to upgrade, most urgent first: running → target version, release notes and why; filter by owner, environment or urgency |
| `changes` | What changed recently (default: last 24 hours) |
| `promotions` | What waits for the next environment, with the releases it brings |
| `delivery` | Deploys per environment and lead times, last 30 days |
| `inventory` | Every running container with image and digest, now or at a past time |
| `hygiene` | Moving tags, tags pushed again, untrusted registries |
| `vulnerabilities` | Is a CVE running? Known vulnerabilities of running images, exploited ones first, with the services and the version that fixes each |
| `acknowledge` | Quiet notifications until a version or a time (needs a member token; the only tool that writes) |

## Remote (HTTP)

Every Goliash server serves MCP at `/mcp` (Streamable HTTP), authenticated with the token.

```sh
claude mcp add --transport http goliash https://goliash.example.com/mcp \
  --header "Authorization: Bearer glsh_api_…"
```

For Cursor, in `.cursor/mcp.json`:

```json
{
  "mcpServers": {
    "goliash": {
      "url": "https://goliash.example.com/mcp",
      "headers": { "Authorization": "Bearer glsh_api_…" }
    }
  }
}
```

## Local (stdio)

`goliash mcp` runs an MCP server on stdin and stdout that talks to your Goliash server. For Claude Desktop, in
`claude_desktop_config.json`:

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
