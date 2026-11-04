<script lang="ts">
  import { untrack } from 'svelte'
  import { api, ApiError } from '../lib/api/client'
  import type { Listing } from '../lib/api/types'
  import { displayName } from '../lib/fname'
  import { formatBytes, formatDate } from '../lib/format'
  import { listingMeta, listingSummary, listingTitle, rememberListing } from '../lib/listings'
  import { t } from '../lib/i18n/en'
  import Button from '../components/ui/Button.svelte'
  import EmptyState from '../components/ui/EmptyState.svelte'
  import Pill from '../components/ui/Pill.svelte'
  import VirtualList from '../components/ui/VirtualList.svelte'

  let { id }: { id: number } = $props()

  let info = $state<Listing | null>(null)
  let items = $state.raw<Listing['items']>([])
  let cursor = $state<string | null>(null)
  let expired = $state(false)
  let failed = $state(false)
  let loadingMore = $state(false)
  let timer: ReturnType<typeof setTimeout> | undefined
  let gen = 0

  const meta = $derived(listingMeta(id) ?? { id })

  type Item = Listing['items'][number]
  const cols = ['type', 'name', 'size', 'alloc', 'mtime', 'uid'] as const
  type Col = (typeof cols)[number]
  let sort = $state<{ col: Col; dir: 1 | -1 } | null>(null)

  // Text columns start ascending, number columns with the biggest.
  function sortBy(col: Col) {
    if (sort?.col === col) sort = { col, dir: sort.dir === 1 ? -1 : 1 }
    else sort = { col, dir: col === 'name' || col === 'type' ? 1 : -1 }
  }

  const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' })
  const rows = $derived.by(() => {
    if (!sort) return items
    const { col, dir } = sort
    const cmp = (a: Item, b: Item) =>
      col === 'name' || col === 'type' ? collator.compare(displayName(a[col]), displayName(b[col])) : a[col] - b[col]
    return [...items].sort((a, b) => dir * cmp(a, b) || collator.compare(a.name, b.name))
  })

  // A sort needs every row, so a sorted view pulls the remaining pages in.
  $effect(() => {
    if (sort && cursor && !loadingMore && info && !active(info.status)) void fetchPage(true)
  })

  const active = (s: string) => s === 'queued' || s === 'running'

  function schedule() {
    clearTimeout(timer)
    timer = setTimeout(() => void fetchPage(false), 2000)
  }

  async function fetchPage(more: boolean) {
    if (more && loadingMore) return
    const mine = more ? gen : ++gen
    loadingMore = more
    try {
      const p = await api.get<Listing>(`/listings/${id}`, { cursor: more ? (cursor ?? undefined) : undefined })
      if (mine !== gen) return
      failed = false
      // Opening a listing by its link adds it to this tab's list.
      rememberListing(id)
      info = p
      items = more ? [...items, ...p.items] : p.items
      cursor = p.next_cursor
      if (active(p.status)) schedule()
    } catch (e) {
      if (mine !== gen) return
      if (e instanceof ApiError && e.status === 404) expired = true
      else {
        failed = true
        if (info && active(info.status)) schedule()
      }
    } finally {
      loadingMore = false
    }
  }

  $effect(() => {
    void id
    untrack(() => {
      info = null
      items = []
      cursor = null
      expired = false
      failed = false
      void fetchPage(false)
    })
    return () => {
      gen++
      clearTimeout(timer)
    }
  })

  async function cancel() {
    try {
      await api.del(`/listings/${id}`)
    } catch {
      /* the next fetch shows the real state */
    }
    clearTimeout(timer)
    void fetchPage(false)
  }

  function need(_s: number, end: number) {
    if (cursor && !loadingMore && info && !active(info.status) && end >= rows.length - 20) void fetchPage(true)
  }
</script>

<section class="page">
  <div class="top">
    <h1>{listingTitle(meta)}</h1>
    <a class="all" href="/listings">{t.listing.all}</a>
  </div>
  {#if listingSummary(meta)}<p class="sub">{listingSummary(meta)}</p>{/if}
  {#if expired}
    <EmptyState title={t.listing.title} body={t.listing.expired}>
      <a href="/listings">{t.nav.listings}</a>
    </EmptyState>
  {:else if failed && !info}
    <EmptyState title={t.common.error} body={t.listing.pageFailed}>
      <Button onclick={() => fetchPage(false)}>{t.common.retry}</Button>
    </EmptyState>
  {:else if !info}
    <p>{t.common.loading}</p>
  {:else}
    <div class="status">
      <Pill tone={info.status === 'done' ? 'ok' : info.status === 'failed' ? 'danger' : 'neutral'}>{t.listing.status[info.status] ?? info.status}</Pill>
      {#if active(info.status)}<Button variant="ghost" onclick={cancel}>{t.listing.cancel}</Button>{/if}
    </div>
    {#if failed}<p class="error" role="alert">{t.listing.pageFailed}</p>{/if}
    {#if info.truncated}<p class="banner" role="status">{t.listing.truncated}</p>{/if}
    {#if info.error}<p class="error" role="alert">{info.error}</p>{/if}
    {#if info.status === 'done' && items.length === 0}
      <EmptyState title={t.listing.title} body={t.listing.empty} />
    {:else if items.length > 0}
      <div class="head" role="row">
        {#each cols as c (c)}
          {@const on = sort?.col === c}
          <button
            class="h {c}"
            class:on
            class:r={c === 'size' || c === 'alloc' || c === 'uid'}
            aria-label={t.list.sortBy(t.listing.cols[c])}
            aria-sort={on ? (sort?.dir === 1 ? 'ascending' : 'descending') : 'none'}
            onclick={() => sortBy(c)}
          >
            {t.listing.cols[c]}<span class="arrow" aria-hidden="true">{on ? (sort?.dir === 1 ? '↑' : '↓') : ''}</span>
          </button>
        {/each}
      </div>
      <VirtualList count={rows.length + (cursor ? 1 : 0)} rowHeight={32} height={520} label={t.listing.title} load={need}>
        {#snippet row(i)}
          {@const it = rows[i]}
          {#if it}
            <div class="row">
              <span class="ty" title={it.type}>
                {#if it.type === 'dir'}
                  <svg viewBox="0 0 16 16" width="16" height="16" class="dir" role="img" aria-label="dir"><path d="M1.5 4.5a1 1 0 0 1 1-1h3.2l1.5 1.5h6.3a1 1 0 0 1 1 1v6a1 1 0 0 1-1 1h-11a1 1 0 0 1-1-1z" fill="currentColor" /></svg>
                {:else if it.type === 'file'}
                  <svg viewBox="0 0 16 16" width="16" height="16" class="file" role="img" aria-label="file"><path d="M4 1.75h5l3.25 3.25v8.5a.75.75 0 0 1-.75.75h-7.5a.75.75 0 0 1-.75-.75v-11a.75.75 0 0 1 .75-.75zM9 1.75V5h3.25" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round" /></svg>
                {:else}
                  <svg viewBox="0 0 16 16" width="16" height="16" class="other" role="img" aria-label={it.type}><circle cx="8" cy="8" r="5.5" fill="none" stroke="currentColor" stroke-width="1.3" stroke-dasharray="2.5 2" /></svg>
                {/if}
              </span>
              <span class="name" style="unicode-bidi: isolate" title={displayName(it.name)}>{displayName(it.name)}</span>
              <span class="mono r">{formatBytes(it.size)}</span>
              <span class="mono r">{formatBytes(it.alloc)}</span>
              <span class="mono">{formatDate(it.mtime)}</span>
              <span class="mono r">{it.uid}</span>
            </div>
          {:else}
            <span class="more">{t.list.more}</span>
          {/if}
        {/snippet}
      </VirtualList>
    {/if}
  {/if}
</section>

<style>
  .page {
    padding: 16px 20px;
  }
  .top {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 12px;
    margin-bottom: 4px;
  }
  h1 {
    font: 600 24px var(--serif);
    margin: 0;
  }
  .sub {
    margin: 0 0 12px;
    color: var(--ink-2);
  }
  .status {
    display: flex;
    gap: 10px;
    align-items: center;
    margin-bottom: 10px;
  }
  .banner {
    background: var(--mustard-bg);
    border: var(--edge);
    border-radius: 6px;
    padding: 6px 10px;
  }
  .error {
    color: var(--danger);
  }
  .head,
  .row {
    display: grid;
    grid-template-columns: 44px minmax(0, 1fr) 90px 90px 110px 70px;
    gap: 8px 16px;
    padding: 0 6px;
    align-items: center;
    height: 100%;
  }
  .head {
    height: 28px;
    border-bottom: 1px solid var(--hair);
    font-size: 12px;
    /* Reserve the width of the list's scrollbar, invisibly, so the headers sit over their columns. */
    overflow-x: hidden;
    overflow-y: scroll;
    scrollbar-color: transparent transparent;
  }
  .h {
    border: none;
    background: none;
    cursor: pointer;
    font: inherit;
    text-align: left;
    padding: 0;
    color: var(--ink-2);
    white-space: nowrap;
  }
  .h.type {
    text-align: center;
  }
  .h.on {
    color: var(--ink);
    font-weight: 500;
  }
  .arrow {
    display: inline-block;
    width: 1em;
  }
  .ty {
    display: flex;
    justify-content: center;
  }
  .dir {
    color: var(--mustard);
  }
  .file,
  .other {
    color: var(--ink-2);
  }
  .r {
    text-align: right;
  }
  .name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
</style>
