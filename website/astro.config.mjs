// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only
import { defineConfig, passthroughImageService } from 'astro/config';
import starlight from '@astrojs/starlight';

export default defineConfig({
  site: 'https://goliash.dev',
  trailingSlash: 'always',
  image: { service: passthroughImageService() },
  integrations: [
    starlight({
      title: 'Goliash',
      description: 'What runs where, on which version — across Kubernetes, ECS, Nomad, Docker Swarm and Docker.',
      logo: { src: './src/assets/logo.svg', alt: 'Goliash' },
      favicon: '/favicon.svg',
      social: [{ icon: 'github', label: 'GitHub', href: 'https://github.com/pipozzz/goliash' }],
      editLink: { baseUrl: 'https://github.com/pipozzz/goliash/edit/main/website/' },
      lastUpdated: true,
      customCss: ['./src/styles/theme.css'],
      head: [
        // Sharing previews (Open Graph, X/Twitter, Slack, LinkedIn).
        { tag: 'meta', attrs: { property: 'og:image', content: 'https://goliash.dev/og.png' } },
        { tag: 'meta', attrs: { property: 'og:image:width', content: '1200' } },
        { tag: 'meta', attrs: { property: 'og:image:height', content: '630' } },
        { tag: 'meta', attrs: { property: 'og:image:alt', content: 'Goliash: what runs where, on which version. The service × environment matrix.' } },
        { tag: 'meta', attrs: { name: 'twitter:image', content: 'https://goliash.dev/og.png' } },
        { tag: 'meta', attrs: { name: 'theme-color', content: '#1b2a6b' } },
        { tag: 'link', attrs: { rel: 'apple-touch-icon', href: '/apple-touch-icon.png' } },
        // What search engines show for the project (schema.org).
        {
          tag: 'script',
          attrs: { type: 'application/ld+json' },
          content: JSON.stringify({
            '@context': 'https://schema.org',
            '@type': 'SoftwareApplication',
            name: 'Goliash',
            url: 'https://goliash.dev/',
            description:
              'Self-hosted, open-source version tracking: one matrix of services × environments across Kubernetes, Amazon ECS, Nomad, Docker Swarm and Compose, with upstream releases, drift and end-of-life.',
            applicationCategory: 'DeveloperApplication',
            operatingSystem: 'Linux, macOS, Windows',
            license: 'https://github.com/pipozzz/goliash/blob/main/LICENSING.md',
            codeRepository: 'https://github.com/pipozzz/goliash',
            image: 'https://goliash.dev/og.png',
            offers: { '@type': 'Offer', price: '0', priceCurrency: 'USD' },
          }),
        },
        // Privacy-friendly analytics by Plausible (no cookies).
        { tag: 'script', attrs: { async: true, src: 'https://plausible.pipoline.com/js/pa-UiQoELmt_sImYLWZXbRAj.js' } },
        {
          tag: 'script',
          content:
            'window.plausible=window.plausible||function(){(plausible.q=plausible.q||[]).push(arguments)},plausible.init=plausible.init||function(i){plausible.o=i||{}};plausible.init()',
        },
      ],
      sidebar: [
        { label: 'Getting started', slug: 'getting-started' },
        {
          label: 'Install',
          items: [
            { label: 'Overview', slug: 'install' },
            { slug: 'install/kubernetes' },
            { slug: 'install/docker' },
            { slug: 'install/swarm' },
            { slug: 'install/nomad' },
            { slug: 'install/ecs' },
          ],
        },
        {
          label: 'Guide',
          items: [{ slug: 'guide/matrix' }, { slug: 'guide/apps' }, { slug: 'guide/versions' }, { slug: 'guide/notifications' }, { slug: 'guide/security' }, { slug: 'guide/observability' }, { slug: 'guide/teams' }],
        },
        {
          label: 'Reference',
          items: [
            { slug: 'reference/configuration' },
            { slug: 'reference/cli' },
            { slug: 'reference/api' },
            { slug: 'reference/mcp' },
            { slug: 'reference/compatibility' },
            { slug: 'reference/collectors' },
            { slug: 'reference/agent-protocol' },
          ],
        },
        { slug: 'security' },
      ],
    }),
  ],
});
