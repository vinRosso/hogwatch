<script lang="ts">
  import { onMount } from 'svelte'
  import { api } from '../../lib/api/client'
  import { t } from '../../lib/i18n/en'
  import { match, navigate, router } from '../../lib/router.svelte'
  import NotFound from '../NotFound.svelte'
  import Audit from './Audit.svelte'
  import Categories from './Categories.svelte'
  import Issues from './Issues.svelte'
  import Mounts from './Mounts.svelte'
  import RootEdit from './RootEdit.svelte'
  import Scans from './Scans.svelte'
  import Settings from './Settings.svelte'
  import UserEdit from './UserEdit.svelte'
  import Users from './Users.svelte'

  const ids = ['users', 'mounts', 'scans', 'issues', 'categories', 'settings', 'audit'] as const
  // Roots are listed on the Mounts page; a root's edit page stays at /admin/roots/:id.
  const section = $derived.by(() => {
    const s = router.path.split('/')[2] ?? 'users'
    return s === 'roots' ? 'mounts' : s
  })
  const userId = $derived(match('/admin/users/:id', router.path)?.id ?? null)
  const rootId = $derived(match('/admin/roots/:id', router.path)?.id ?? null)
  const numeric = (s: string | null) => s !== null && /^[0-9]+$/.test(s)

  // /admin and the former Ranking and Roots pages land on the merged pages.
  const moved: Record<string, string> = { '/admin': '/admin/users', '/admin/ranking': '/admin/users', '/admin/roots': '/admin/mounts' }
  $effect(() => {
    const to = moved[router.path]
    if (to) navigate(to + location.search, { replace: true })
  })

  // The badge counts the issues present when the page opened.
  let issueCount = $state<number | null>(null)
  onMount(() => {
    api.get<{ items: unknown[] }>('/admin/config-issues').then(
      (p) => (issueCount = p.items.length),
      () => {},
    )
  })
</script>

<div class="admin">
  <nav aria-label={t.admin.nav}>
    {#each ids as id (id)}
      <a href={`/admin/${id}`} class:on={section === id} aria-current={section === id ? 'page' : undefined}>
        {t.admin.sections[id]}
        {#if id === 'issues' && issueCount}<span class="badge">{issueCount}</span>{/if}
      </a>
    {/each}
  </nav>
  <div class="body">
    {#if router.path === '/admin/users'}
      <Users />
    {:else if router.path === '/admin/mounts'}
      <Mounts />
    {:else if moved[router.path]}
      <!-- redirecting -->
    {:else if router.path === '/admin/scans'}
      <Scans />
    {:else if router.path === '/admin/issues'}
      <Issues />
    {:else if router.path === '/admin/categories'}
      <Categories />
    {:else if router.path === '/admin/settings'}
      <Settings />
    {:else if router.path === '/admin/audit'}
      <Audit />
    {:else if numeric(rootId)}
      {#key rootId}<RootEdit id={Number(rootId)} />{/key}
    {:else if numeric(userId)}
      {#key userId}<UserEdit id={Number(userId)} />{/key}
    {:else}
      <NotFound />
    {/if}
  </div>
</div>

<style>
  .admin {
    display: grid;
    grid-template-columns: 180px minmax(0, 1fr);
    gap: 16px;
    padding: 16px 20px;
  }
  nav {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  nav a {
    padding: 5px 10px;
    border-radius: 6px;
    border: 1px solid transparent;
    color: var(--ink);
    text-decoration: none;
  }
  nav a.on {
    border-color: var(--ink);
    background: var(--mustard-bg);
  }
  .badge {
    display: inline-block;
    min-width: 18px;
    padding: 0 5px;
    margin-left: 4px;
    border: var(--edge);
    border-radius: var(--r-pill);
    background: var(--danger);
    color: var(--paper);
    font-size: 12px;
    text-align: center;
  }
  /* Phones: the sections wrap in a strip above the page, so the page keeps the full width. */
  @media (max-width: 700px) {
    .admin {
      grid-template-columns: minmax(0, 1fr);
      padding: 12px 16px;
    }
    nav {
      flex-direction: row;
      flex-wrap: wrap;
      gap: 4px;
    }
    nav a {
      padding: 8px 10px;
    }
  }
</style>
