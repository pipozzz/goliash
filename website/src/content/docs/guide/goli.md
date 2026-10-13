---
title: Ask Goli
description: "Goli, the assistant inside Goliash, answers questions about what runs where, what to upgrade and what is exposed, from live data and as the person asking, through Claude."
---

**Goli** is the assistant inside Goliash. Ask it in plain words and it looks things up for you:

- "What should we upgrade first, and why?"
- "Which running images have vulnerabilities exploited in the wild?"
- "What drifted between staging and production this week?"
- "Which version of payments ran in prod last Friday at 18:00?"
- "Is any of the data stale right now?"

Goli answers from the same tools as the [MCP server](/reference/mcp/), called **as you**: it sees exactly what
you may see in the current workspace, and it only reads. It cannot acknowledge drift, change policies or touch
anything else; when you ask it to, it tells you where in Goliash to do it. Answers link to the pages that show
the details, so checking them is one click away.

## Ask

Click **Ask Goli** in the header, or press <kbd>Ctrl</kbd>+<kbd>K</kbd> (<kbd>⌘</kbd>+<kbd>K</kbd>), type a
question and pick **Ask Goli**. The panel stays open while you move between pages; Goli is told which page you are
on, so "why is this red?" works. The conversation lives in the browser tab until you start a new one; Goliash keeps
no copy of it.

## Brief me on this upgrade

On the [Updates](/guide/versions/) page every upgrade has a **Brief** button: Goli reads the release notes of every
release between what runs and the version to move to, from the project's GitHub or GitLab releases, and tells you
what breaks or needs migrating, what it fixes (security first) and what to check. It says so when the notes are
silent about something, and it never calls an upgrade safe: the decision stays yours.

## Turn it on

Goli is off until the server has an API key for Claude, by [Anthropic](https://www.anthropic.com):

```sh
GOLIASH_AI_API_KEY=sk-ant-… goliash serve
```

| Variable | Default | Meaning |
| --- | --- | --- |
| `GOLIASH_AI_API_KEY` | | Anthropic API key; Goli is off without it. `GOLIASH_AI_API_KEY_FILE` reads it from a file instead |
| `GOLIASH_AI_MODEL` | `claude-opus-5-5` | The Claude model |
| `GOLIASH_AI_EFFORT` | `medium` | How hard the model thinks: `low`, `medium` or `high` |
| `GOLIASH_AI_BASE_URL` | Anthropic's API | A gateway or proxy in front of the Anthropic API |
| `GOLIASH_AI_PER_HOUR` | `60` | Questions one person may ask per hour |

An `ANTHROPIC_API_KEY` that happens to be in the server's environment does not turn Goli on: it is a choice to
make on purpose. The public demo never offers it.

## What leaves your server

Your question, the last turns of the conversation, the workspace's name, your role, the page you are on and
**what Goli looks up** (service names, versions, environments, images, vulnerabilities, release notes) go to
Anthropic's API over TLS. Nothing else does: no credentials, tokens, registry secrets or data of other workspaces.
Anthropic does not train on API traffic by default; see its
[commercial terms](https://www.anthropic.com/legal/commercial-terms) and, for zero data retention or a regional
deployment, talk to Anthropic or put your own gateway in `GOLIASH_AI_BASE_URL`.

The server log records who asked, in which workspace, which tools Goli used and how long it took, never the
question.

## Good to know

- **Check what matters.** Goli bases its answers on what the tools return and is told never to invent versions,
  services or CVEs, but a language model can still be wrong. Follow its links before you act.
- **Text in your data is data.** Release notes, labels and image names are shown to Goli as data, never as
  instructions; and since it can only read, a crafted release note cannot make it change anything.
- **Costs** are per question, on your Anthropic account: a question usually takes two to four model calls. The
  instructions and tool descriptions are cached, which makes follow-ups cheaper.
