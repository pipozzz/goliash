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
| `GOLIASH_AI_PROVIDER` | `anthropic` | `anthropic`, or `openai` for any OpenAI-compatible API (see below) |
| `GOLIASH_AI_API_KEY` | | The provider's API key; Goli is off without it. `GOLIASH_AI_API_KEY_FILE` reads it from a file instead |
| `GOLIASH_AI_MODEL` | `claude-opus-5-5` | The model; required with `openai` |
| `GOLIASH_AI_EFFORT` | `medium` | How hard the model thinks: `low`, `medium` or `high` (sent to OpenAI-compatible servers only when set) |
| `GOLIASH_AI_BASE_URL` | the provider's API | A gateway or proxy, or the OpenAI-compatible server |
| `GOLIASH_AI_API` | `responses` for OpenAI, `chat` with a base URL | With `openai`: OpenAI's Responses API, which its newer models need for tool calls, or Chat Completions, which most other servers speak |
| `GOLIASH_AI_PER_HOUR` | `60` | Questions one person may ask per hour |
| `GOLIASH_AI_PLAN_SUMMARY` | off | `true` opens each [upgrade plan](/guide/notifications/#upgrade-plan) with Goli's take on it |
| `GOLIASH_AI_PER_DAY` | `0` (none) | Questions everyone together may ask per day on one server: a ceiling on the cost. Each server of a cluster counts its own; the count starts over at midnight UTC |

For a hard limit on spending, also give Goliash its own key with a budget at the provider: a separate workspace
with a monthly spend limit in the Anthropic Console, or a project with a budget at OpenAI. When it runs out, Goli
says the limit is reached.

### OpenAI and other models

Goli also talks to any server that speaks the OpenAI Chat Completions API with function calling: OpenAI itself,
Azure OpenAI or Bedrock through a gateway such as LiteLLM, OpenRouter, Groq, or a model on your own hardware with
Ollama or vLLM. Name the model it serves:

```sh
# OpenAI
GOLIASH_AI_PROVIDER=openai GOLIASH_AI_API_KEY=sk-… GOLIASH_AI_MODEL=<model> goliash serve

# A local model with Ollama: nothing leaves your network, and no key is needed
GOLIASH_AI_PROVIDER=openai GOLIASH_AI_BASE_URL=http://ollama:11434/v1 GOLIASH_AI_MODEL=<model> goliash serve
```

With OpenAI itself Goli uses the Responses API and asks OpenAI to keep nothing (`store: false`). With a base URL it
speaks Chat Completions; set `GOLIASH_AI_API=responses` for a gateway that passes the Responses API through.

Goli is only as good as the model behind it: pick one that handles tool calls well, or answers get vague. The panel
and the Status page say which provider answers.

With the Helm chart, keep the key in a Secret:

```sh
kubectl create secret generic goliash-ai --from-literal=anthropic-api-key=sk-ant-…
helm upgrade goliash oci://ghcr.io/pipozzz/charts/goliash --reuse-values --set goli.existingSecret=goliash-ai
```

The pods then need egress to `api.anthropic.com` (or your `goli.baseURL`).

An `ANTHROPIC_API_KEY` or `OPENAI_API_KEY` that happens to be in the server's environment does not turn Goli on: it is a choice to
make on purpose.

### On a public demo

A public demo (`goliash try -public`) offers Goli only with `GOLIASH_DEMO_GOLI=true`. Everyone there is the same
viewer, so the hourly limit counts per visitor address instead (set `GOLIASH_TRUST_PROXY=true` behind a proxy), Goli
reads a third as much of each tool result, and it is told to stay on Goliash and the example data. Give it a daily
ceiling (`GOLIASH_AI_PER_DAY`) and a key with its own budget: on a demo, anyone pays with your money.

## What leaves your server

Your question, the last turns of the conversation, the workspace's name, your role, the page you are on and
**what Goli looks up** (service names, versions, environments, images, vulnerabilities, release notes) go to
the model's API: Anthropic's by default, or the provider you configure. Nothing else does: no credentials, tokens, registry secrets or data of other workspaces.
Anthropic does not train on API traffic by default; see its
[commercial terms](https://www.anthropic.com/legal/commercial-terms) and, for zero data retention or a regional
deployment, talk to Anthropic or put your own gateway in `GOLIASH_AI_BASE_URL`. With a local model, nothing leaves
your network.

The server log records who asked, in which workspace, which tools Goli used and how long it took, never the
question.

## Good to know

- **Check what matters.** Goli bases its answers on what the tools return and is told never to invent versions,
  services or CVEs, but a language model can still be wrong. Follow its links before you act.
- **Text in your data is data.** Release notes, labels and image names are shown to Goli as data, never as
  instructions; and since it can only read, a crafted release note cannot make it change anything.
- **Costs** are per question, on your Anthropic account: a question usually takes two to four model calls. The
  instructions and tool descriptions are cached, which makes follow-ups cheaper.

## Goli on your website

Goli can also answer questions about a product from its documentation, as it does on goliash.dev: no access to any
data, only the text you give it. Point a server that has `GOLIASH_AI_API_KEY` at the documentation as one plain text
(an [llms-full.txt](https://llmstxt.org)) and name the sites that may ask:

| Variable | Default | Meaning |
| --- | --- | --- |
| `GOLIASH_DOCS_ASSISTANT` | | URL of the documentation text; read again every hour. Turns on `POST /goli/docs` |
| `GOLIASH_DOCS_ASSISTANT_ORIGINS` | `https://goliash.dev` | Comma-separated sites whose pages may ask (CORS) |
| `GOLIASH_DOCS_ASSISTANT_PER_HOUR` | `20` | Questions one visitor may ask per hour (set `GOLIASH_TRUST_PROXY=true` behind a proxy) |
| `GOLIASH_DOCS_ASSISTANT_PER_DAY` | `500` | Questions everyone together may ask per day: a ceiling on the cost |
| `GOLIASH_DOCS_ASSISTANT_MODEL` | Goli's model | A model of its own for the documentation, often a cheaper one |

The endpoint reads no cookies and changes nothing; questions are not stored or logged. The documentation is cached by
Claude between questions, so each one costs little more than its answer.
