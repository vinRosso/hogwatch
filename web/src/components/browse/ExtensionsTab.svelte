<script lang="ts">
  import { api } from '../../lib/api/client'
  import type { ExtensionStat } from '../../lib/api/types'
  import { displayName } from '../../lib/fname'
  import { formatBytes, formatCount } from '../../lib/format'
  import { t } from '../../lib/i18n/en'
  import { session } from '../../lib/session.svelte'
  import Button from '../ui/Button.svelte'
  import EmptyState from '../ui/EmptyState.svelte'

  // files is the node's file count: an empty list means "no files" only when
  // it is 0; otherwise the scanner kept no totals for this folder.
  let { ref, files, onlist }: { ref: string; files: number; onlist?: (ref: string) => void } = $props()
  const canList = $derived(session.canListFiles() && !!onlist)

  let items = $state<ExtensionStat[] | null>(null)
  let failed = $state(false)
  let seq = 0

  $effect(() => {
    const mine = ++seq
    items = null
    failed = false
    api
      .get<{ items: ExtensionStat[] }>(`/nodes/${ref}/extensions`)
      .then((x) => mine === seq && (items = x.items))
      .catch(() => mine === seq && (failed = true))
  })

  const max = $derived(Math.max(1, ...(items ?? []).map((i) => i.bytes_alloc)))
</script>

{#if failed}
  <EmptyState title={t.common.error} body={t.list.failed} />
{:else if items && items.length === 0}
  {#if files === 0}
    <EmptyState title={t.tabs.ext} body={t.ext.empty} />
  {:else}
    <EmptyState title={t.tabs.ext} body={canList ? t.ext.notStoredCanList : t.ext.notStored}>
      {#if canList}<Button onclick={() => onlist?.(ref)}>{t.ext.listFiles}</Button>{/if}
    </EmptyState>
  {/if}
{:else if items}
  <table>
    <thead>
      <tr><th>{t.ext.ext}</th><th>{t.ext.category}</th><th class="r">{t.ext.files}</th><th class="r">{t.ext.size}</th><th></th></tr>
    </thead>
    <tbody>
      {#each items as it (it.ext + it.category)}
        <tr>
          <td class="mono" style="unicode-bidi: isolate">{it.ext ? displayName(it.ext) : t.ext.none}</td>
          <td>{t.filter.cat[it.category] ?? it.category}</td>
          <td class="mono r">{formatCount(it.files)}</td>
          <td class="mono r">{formatBytes(it.bytes_alloc)}</td>
          <td class="bar"><span style:width={`${(it.bytes_alloc / max) * 100}%`}></span></td>
        </tr>
      {/each}
    </tbody>
  </table>
{/if}

<style>
  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 13px;
  }
  th {
    text-align: left;
    color: var(--ink-2);
    font-weight: 400;
    padding: 4px;
    border-bottom: 1px solid var(--hair);
  }
  td {
    padding: 4px;
    border-bottom: 1px solid var(--hair);
  }
  .r {
    text-align: right;
  }
  .bar {
    width: 60px;
  }
  .bar span {
    display: block;
    height: 8px;
    background: var(--terra);
    border: 1px solid var(--ink);
    border-radius: 4px;
  }
</style>
