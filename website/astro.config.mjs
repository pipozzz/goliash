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
          items: [{ slug: 'guide/matrix' }, { slug: 'guide/versions' }, { slug: 'guide/notifications' }, { slug: 'guide/teams' }],
        },
        {
          label: 'Reference',
          items: [
            { slug: 'reference/configuration' },
            { slug: 'reference/cli' },
            { slug: 'reference/api' },
            { slug: 'reference/mcp' },
            { slug: 'reference/collectors' },
            { slug: 'reference/agent-protocol' },
          ],
        },
        { slug: 'security' },
      ],
    }),
  ],
});
