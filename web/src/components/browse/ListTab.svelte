<script lang="ts">
  import { untrack } from 'svelte'
  import { api } from '../../lib/api/client'
  import type { Child, ChildrenPage } from '../../lib/api/types'
  import { browse } from '../../lib/browse.svelte'
  import { displayName } from '../../lib/fname'
  import { formatAge, formatBytes, formatCount } from '../../lib/format'
  import { t } from '../../lib/i18n/en'
  import { ownerOf } from '../../lib/owners.svelte'
  import { session } from '../../lib/session.svelte'
  import { blockVar, ownerColor } from '../../lib/userColor'
  import EmptyState from '../ui/EmptyState.svelte'
  import VirtualList from '../ui/VirtualList.svelte'

  let { ref, filter }: { ref: string; filter: string } = $props()

  const sorts = ['size', 'name', 'mtime', 'files'] as const
  type Sort = (typeof sorts)[number]
  let sort = $state<Sort>('size')
  let items = $state.raw<Child[]>([])
  let cursor = $state<string | null>(null)
  let loading = $state(false)
  let failed = $state(false)
  let loaded = $state(false)
  let seq = 0

  async function fetchPage(reset: boolean) {
    if (loading && !reset) return
    const mine = reset ? ++seq : seq
    loading = true
    failed = false
    try {
      const page = await api.get<ChildrenPage>(`/nodes/${ref}/children`, {
        sort,
        limit: 200,
        filter: filter || undefined,
        cursor: reset ? undefined : (cursor ?? undefined),
      })
      if (mine !== seq) return
      items = reset ? page.items : [...items, ...page.items]
      cursor = page.next_cursor
      loaded = true
    } catch {
      if (mine === seq) failed = true
    } finally {
      if (mine === seq) loading = false
    }
  }

  $effect(() => {
    void ref
    void filter
    void sort
    untrack(() => {
      items = []
      cursor = null
      loaded = false
      void fetchPage(true)
    })
  })

  function need(_start: number, end: number) {
    if (cursor && !loading && end >= items.length - 20) void fetchPage(false)
  }

  const denom = $derived(filter ? Math.max(1, ...items.map((i) => i.bytes_alloc)) : Math.max(1, browse.node?.bytes_alloc ?? 1))
  // Non-admins see the treemap's color for each row: the same cycle position, in the order the rows load.
  // A folder takes its user's color; the files row and folders no user path covers take the unowned grey.
  const swatch = (c: Child) => {
    if (!session.me?.is_admin) return `var(${blockVar(items.indexOf(c))})`
    const node = browse.node
    if (!node) return 'var(--unowned)'
    const path = c.kind === 'files' ? node.rel_path : [node.rel_path, c.name].filter(Boolean).join('/')
    return ownerColor(ownerOf(node.root_id, path))
  }

  const label = (c: Child) => (c.kind === 'files' ? t.list.files_kind : displayName(c.name))
</script>

<div class="head" role="row">
  <span></span>
  {#each [['name', t.list.name], ['size', t.list.size], ['share', t.list.share], ['files', t.list.files], ['mtime', t.list.newest]] as [id, text] (id)}
    {#if id === 'share'}
      <span class="h">{text}</span>
    {:else}
      <button class="h" class:on={sort === id} aria-pressed={sort === id} aria-label={t.list.sortBy(text)} onclick={() => (sort = id as Sort)}>
        {text}
      </button>
    {/if}
  {/each}
</div>

{#if failed}
  <EmptyState title={t.common.error} body={t.list.failed} />
{:else if loaded && items.length === 0}
  <EmptyState title={t.nav.browse} body={t.list.empty} />
{:else}
  <VirtualList count={items.length + (cursor ? 1 : 0)} rowHeight={32} height={420} label={t.tabs.list} load={need}>
    {#snippet row(i)}
      {@const c = items[i]}
      {#if c}
        <button
          class="row"
          onclick={() => c.kind === 'dir' && c.ref && browse.open(c.ref)}
          disabled={c.kind !== 'dir' || !c.ref}
        >
          <span class="sw" style:background={swatch(c)}></span>
          <span class="name" style="unicode-bidi: isolate" title={label(c)}>{label(c)}</span>
          <span class="mono num">{formatBytes(c.bytes_alloc)}</span>
          <span class="bar"><span style:width={`${Math.min(100, (c.bytes_alloc / denom) * 100)}%`}></span></span>
          <span class="mono num">{formatCount(c.files)}</span>
          <span class="mono num">{formatAge(c.mtime, Math.floor(Date.now() / 1000))}</span>
        </button>
      {:else}
        <span class="more">{t.list.more}</span>
      {/if}
    {/snippet}
  </VirtualList>
{/if}

<style>
  .head {
    display: grid;
    grid-template-columns: 14px minmax(0, 1fr) 70px 60px 56px 76px;
    gap: 6px;
    padding: 4px 6px;
    border-bottom: 1px solid var(--hair);
    font-size: 12px;
  }
  .h {
    border: none;
    background: none;
    cursor: pointer;
    text-align: left;
    padding: 0;
    color: var(--ink-2);
  }
  .h.on {
    color: var(--ink);
    font-weight: 500;
    text-decoration: underline;
  }
  .row {
    display: grid;
    grid-template-columns: 14px minmax(0, 1fr) 70px 60px 56px 76px;
    gap: 6px;
    align-items: center;
    width: 100%;
    height: 100%;
    padding: 0 6px;
    border: none;
    background: none;
    text-align: left;
    cursor: pointer;
  }
  .row:hover,
  .row:focus-visible {
    background: var(--sunk);
  }
  .row:disabled {
    cursor: default;
    color: var(--ink-2);
  }
  .sw {
    width: 12px;
    height: 12px;
    border: var(--edge);
    border-radius: 3px;
  }
  .name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .num {
    font-size: 12px;
    text-align: right;
  }
  .bar {
    height: 8px;
    border: 1px solid var(--ink);
    border-radius: 4px;
    background: var(--card);
    overflow: hidden;
  }
  .bar span {
    display: block;
    height: 100%;
    background: var(--terra);
  }
  .more {
    padding: 0 8px;
    color: var(--ink-2);
  }
  /* Phones: the fixed columns would squeeze the name to nothing, so Share and Newest give way. */
  @media (max-width: 480px) {
    .head,
    .row {
      grid-template-columns: 14px minmax(0, 1fr) 70px 56px;
    }
    .head > :nth-child(4),
    .head > :nth-child(6),
    .row > :nth-child(4),
    .row > :nth-child(6) {
      display: none;
    }
  }
</style>
