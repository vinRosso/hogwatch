<script lang="ts">
  import { untrack } from 'svelte'
  import { api } from '../../lib/api/client'
  import type { ErrorPage } from '../../lib/api/types'
  import { browse } from '../../lib/browse.svelte'
  import { errnoMessage } from '../../lib/errno'
  import { displayName } from '../../lib/fname'
  import { t } from '../../lib/i18n/en'
  import Button from '../ui/Button.svelte'
  import EmptyState from '../ui/EmptyState.svelte'

  let { ref }: { ref: string } = $props()

  let items = $state<ErrorPage['items']>([])
  let cursor = $state<string | null>(null)
  let loaded = $state(false)
  let failed = $state(false)
  let loading = $state(false)
  let seq = 0

  async function page(reset: boolean) {
    const mine = reset ? ++seq : seq
    loading = true
    failed = false
    try {
      const p = await api.get<ErrorPage>(`/nodes/${ref}/errors`, { cursor: reset ? undefined : (cursor ?? undefined) })
      if (mine !== seq) return
      items = reset ? p.items : [...items, ...p.items]
      cursor = p.next_cursor
      loaded = true
    } catch {
      if (mine === seq) failed = true
    } finally {
      if (mine === seq) loading = false
    }
  }

  $effect(() => {
    void ref
    untrack(() => {
      items = []
      cursor = null
      loaded = false
      void page(true)
    })
  })
</script>

<p class="head">{t.errors.header(browse.node?.error_count ?? items.length)}</p>
{#if failed}
  <EmptyState title={t.common.error} body={t.errors.failed} />
{:else if loaded && items.length === 0}
  <EmptyState title={t.tabs.errors} body={t.errors.none} />
{:else}
  <ul>
    {#each items as it, i (i)}
      <li>
        <span class="name" style="unicode-bidi: isolate" title={displayName(it.name)}>{displayName(it.name)}</span>
        <span class="why">{errnoMessage(it.errno)}</span>
      </li>
    {/each}
  </ul>
  {#if cursor}<Button variant="ghost" disabled={loading} onclick={() => page(false)}>{t.errors.more}</Button>{/if}
{/if}

<style>
  .head {
    margin: 8px 0;
    color: var(--ink-2);
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    flex: 1 1 auto;
    min-height: 0;
    overflow: auto;
  }
  li {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    gap: 10px;
    padding: 5px 4px;
    border-bottom: 1px solid var(--hair);
  }
  .name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .why {
    color: var(--ink-2);
  }
</style>
