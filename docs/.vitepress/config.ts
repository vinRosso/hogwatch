import { defineConfig } from 'vitepress'

export default defineConfig({
  title: 'Hogwatch',
  description: 'Self-hosted disk usage analyzer for your storage server.',
  base: '/hogwatch/',
  cleanUrls: true,
  lastUpdated: false,
  head: [['link', { rel: 'icon', type: 'image/svg+xml', href: '/hogwatch/images/hogwatch-logo.svg' }]],
  themeConfig: {
    logo: '/images/hogwatch-logo.svg',
    nav: [
      { text: 'Install', link: '/install' },
      { text: 'GitHub', link: 'https://github.com/vinRosso/hogwatch' },
    ],
    sidebar: [
      {
        text: 'Get started',
        items: [
          { text: 'Install', link: '/install' },
          { text: 'Using Hogwatch', link: '/using' },
          { text: 'Administration', link: '/admin' },
        ],
      },
      {
        text: 'Operate',
        items: [
          { text: 'ZFS', link: '/zfs' },
          { text: 'Single sign-on (OIDC)', link: '/oidc' },
          { text: 'Backup', link: '/backup' },
          { text: 'Troubleshooting', link: '/troubleshooting' },
        ],
      },
      { text: 'Security', items: [{ text: 'Threat model', link: '/security' }] },
    ],
    search: { provider: 'local' },
    socialLinks: [{ icon: 'github', link: 'https://github.com/vinRosso/hogwatch' }],
    footer: { message: 'Released under AGPL-3.0-or-later.' },
  },
})
