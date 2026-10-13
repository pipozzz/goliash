// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only
// The whole documentation as one plain text (llmstxt.org), for AI assistants and for Goli on this site.
import { readFile } from 'node:fs/promises';
import { getCollection } from 'astro:content';

// MDX imports and components carry no prose; Markdown stays as written.
function prose(body: string): string {
  return body
    .replace(/^import .*$/gm, '')
    .replace(/<(Image|Redoc) [^>]*\/>/g, '')
    .replace(/<\/?(CardGrid|Card|LinkCard|Tabs|TabItem|Steps|Aside|div|p)\b[^>]*>/g, '')
    .replace(/\n{3,}/g, '\n\n')
    .trim();
}

export async function GET() {
  const docs = (await getCollection('docs')).sort((a, b) => a.id.localeCompare(b.id));
  const parts = docs.map((d) => {
    const path = d.id === 'index' ? '/' : `/${d.id.replace(/\/index$/, '')}/`;
    return `# ${d.data.title}\nSource: ${path}\n${d.data.description ? `\n> ${d.data.description}\n` : ''}\n${prose(d.body ?? '')}\n`;
  });
  const head = '# Goliash\n\n> What runs where, on which version, in which environment: self-hosted, open-source version tracking for Kubernetes, Amazon ECS, AWS Lambda, Nomad, Docker Swarm and Compose. https://goliash.dev\n\n';
  // The API reference renders the OpenAPI document in the browser; here it is as text, exactly as served.
  const api = await readFile(new URL('../api/public-v1.yaml', `file://${process.cwd()}/`), 'utf8').catch(() => '');
  if (api) parts.push(`# REST API (OpenAPI 3)\nSource: /reference/api/\n\n\`\`\`yaml\n${api.trim()}\n\`\`\`\n`);
  return new Response(head + parts.join('\n---\n\n'), { headers: { 'Content-Type': 'text/plain; charset=utf-8' } });
}
