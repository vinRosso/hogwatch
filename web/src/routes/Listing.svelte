<script lang="ts">
  import { untrack } from 'svelte'
  import { api, ApiError } from '../lib/api/client'
  import type { Listing } from '../lib/api/types'
  import { displayName } from '../lib/fname'
  import { formatBytes, formatDate } from '../lib/format'
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
    if (cursor && !loadingMore && info && !active(info.status) && end >= items.length - 20) void fetchPage(true)
  }
</script>

<section class="page">
  <h1>{t.listing.open(id)}</h1>
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
        <span>{t.listing.cols.name}</span><span>{t.listing.cols.type}</span><span class="r">{t.listing.cols.size}</span>
        <span class="r">{t.listing.cols.alloc}</span><span>{t.listing.cols.mtime}</span><span class="r">{t.listing.cols.uid}</span>
      </div>
      <VirtualList count={items.length + (cursor ? 1 : 0)} rowHeight={32} height={520} label={t.listing.title} load={need}>
        {#snippet row(i)}
          {@const it = items[i]}
          {#if it}
            <div class="row">
              <span class="name" style="unicode-bidi: isolate" title={displayName(it.name)}>{displayName(it.name)}</span>
              <span>{it.type}</span>
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
  h1 {
    font: 600 24px var(--serif);
    margin: 0 0 12px;
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
    grid-template-columns: minmax(0, 1fr) 70px 90px 90px 110px 70px;
    gap: 8px;
    padding: 0 6px;
    align-items: center;
    height: 100%;
  }
  .head {
    height: 28px;
    border-bottom: 1px solid var(--hair);
    color: var(--ink-2);
    font-size: 12px;
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
