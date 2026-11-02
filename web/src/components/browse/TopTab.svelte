<script lang="ts">
  import { api } from '../../lib/api/client'
  import type { TopResult } from '../../lib/api/types'
  import { browse } from '../../lib/browse.svelte'
  import { displayName } from '../../lib/fname'
  import { formatAge, formatBytes } from '../../lib/format'
  import { t } from '../../lib/i18n/en'
  import { session } from '../../lib/session.svelte'
  import Button from '../ui/Button.svelte'
  import EmptyState from '../ui/EmptyState.svelte'

  let { ref, onlist }: { ref: string; onlist?: (ref: string) => void } = $props()

  let kind = $state<'largest' | 'oldest'>('largest')
  let result = $state<TopResult | null>(null)
  let failed = $state(false)
  let seq = 0

  $effect(() => {
    const r = ref
    const k = kind
    const mine = ++seq
    result = null
    failed = false
    api
      .get<TopResult>(`/nodes/${r}/top`, { kind: k })
      .then((x) => mine === seq && (result = x))
      .catch(() => mine === seq && (failed = true))
  })
</script>

<div class="seg" role="group">
  <button aria-pressed={kind === 'largest'} class:on={kind === 'largest'} onclick={() => (kind = 'largest')}>{t.top.largest}</button>
  <button aria-pressed={kind === 'oldest'} class:on={kind === 'oldest'} onclick={() => (kind = 'oldest')}>{t.top.oldest}</button>
</div>

{#if failed}
  <EmptyState title={t.common.error} body={t.list.failed} />
{:else if result && !result.stored}
  <EmptyState title={t.tabs.top} body={t.top.notStored}>
    {#if session.canListFiles() && onlist}<Button onclick={() => onlist(ref)}>{t.top.listFiles}</Button>{/if}
  </EmptyState>
{:else if result && result.items.length === 0}
  <EmptyState title={t.tabs.top} body={t.top.empty} />
{:else if result}
  <ol>
    {#each result.items as it, i (i)}
      <li>
        <span class="name" style="unicode-bidi: isolate" title={displayName(it.name)}><strong>{displayName(it.name)}</strong></span>
        <span class="mono">{formatBytes(it.size)}</span>
        <span class="mono">{formatAge(it.mtime, Math.floor(Date.now() / 1000))}</span>
        <button class="open" onclick={() => browse.open(it.dir_ref)}>{t.top.openFolder}</button>
      </li>
    {/each}
  </ol>
{/if}

<style>
  .seg {
    display: inline-flex;
    border: var(--edge);
    border-radius: var(--r-pill);
    overflow: hidden;
    margin: 8px 0;
  }
  .seg button {
    border: none;
    background: var(--card);
    padding: 3px 12px;
    cursor: pointer;
  }
  .seg button.on {
    background: var(--ink);
    color: var(--paper);
  }
  ol {
    list-style: none;
    margin: 0;
    padding: 0;
    max-height: 420px;
    overflow: auto;
  }
  li {
    display: grid;
    grid-template-columns: minmax(0, 1fr) 70px 76px auto;
    gap: 8px;
    padding: 5px 4px;
    border-bottom: 1px solid var(--hair);
    align-items: center;
  }
  .name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .open {
    border: none;
    background: none;
    color: var(--terra);
    cursor: pointer;
    font-size: 12px;
  }
</style>
