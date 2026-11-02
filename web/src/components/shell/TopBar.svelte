<script lang="ts">
  import { t } from '../../lib/i18n/en'
  import { router } from '../../lib/router.svelte'
  import { session } from '../../lib/session.svelte'
  import QuotaBar from './QuotaBar.svelte'
  import UserMenu from './UserMenu.svelte'

  const links = $derived([
    { href: '/browse', label: t.nav.overview },
    { href: '/usage', label: t.nav.usage },
    { href: '/listings', label: t.nav.listings },
    ...(session.me?.is_admin ? [{ href: '/admin', label: t.nav.admin }] : []),
  ])
  const active = (href: string) => router.path === href || router.path.startsWith(href + '/')
</script>

<header class="bar">
  <a class="brand" href="/browse">{t.appName}</a>
  <nav aria-label={t.nav.main}>
    {#each links as l (l.href)}
      <a href={l.href} class:on={active(l.href)} aria-current={active(l.href) ? 'page' : undefined}>{l.label}</a>
    {/each}
  </nav>
  <span class="grow"></span>
  <QuotaBar />
  <UserMenu />
</header>

<style>
  .bar {
    display: flex;
    align-items: center;
    gap: 16px;
    padding: 10px 20px;
    border-bottom: var(--edge);
    background: var(--card);
    flex-wrap: wrap;
  }
  .brand {
    font: 600 22px/1 var(--serif);
    color: var(--ink);
    text-decoration: none;
  }
  nav {
    display: flex;
    gap: 4px;
  }
  nav a {
    padding: 4px 12px;
    border-radius: var(--r-pill);
    border: 1px solid transparent;
    color: var(--ink);
    text-decoration: none;
  }
  nav a.on {
    border-color: var(--ink);
    background: var(--mustard-bg);
  }
  .grow {
    flex: 1;
  }
</style>
