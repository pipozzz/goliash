// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only
// An index of the documentation for AI assistants (llmstxt.org); llms-full.txt holds all of it.
import { getCollection } from 'astro:content';

export async function GET() {
  const docs = (await getCollection('docs')).sort((a, b) => a.id.localeCompare(b.id));
  const lines = docs.map((d) => {
    const path = d.id === 'index' ? '/' : `/${d.id.replace(/\/index$/, '')}/`;
    return `- [${d.data.title}](https://goliash.dev${path})${d.data.description ? `: ${d.data.description}` : ''}`;
  });
  const text = '# Goliash\n\n> What runs where, on which version, in which environment: self-hosted, open-source version tracking for Kubernetes, Amazon ECS, AWS Lambda, Nomad, Docker Swarm and Compose.\n\nThe whole documentation in one file: https://goliash.dev/llms-full.txt\n\n## Docs\n\n' + lines.join('\n') + '\n';
  return new Response(text, { headers: { 'Content-Type': 'text/plain; charset=utf-8' } });
}
