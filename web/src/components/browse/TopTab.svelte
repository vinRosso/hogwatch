<script lang="ts">
  import { api } from '../../lib/api/client'
  import type { Node, TopResult } from '../../lib/api/types'
  import { browse } from '../../lib/browse.svelte'
  import { displayName } from '../../lib/fname'
  import { formatBytes, formatDate } from '../../lib/format'
  import { hostPathOf } from '../../lib/hostPath'
  import { t } from '../../lib/i18n/en'
  import { session } from '../../lib/session.svelte'
  import { toast } from '../../lib/toast.svelte'
  import Button from '../ui/Button.svelte'
  import EmptyState from '../ui/EmptyState.svelte'

  let { ref, onlist }: { ref: string; onlist?: (ref: string, mode?: 'children' | 'subtree') => void } = $props()

  let kind = $state<'largest' | 'oldest'>('largest')
  let result = $state<TopResult | null>(null)
  let failed = $state(false)
  let seq = 0

  // The item's folder gives its root-relative path; the root's anchors turn that into a host path.
  async function copyPath(it: TopResult['items'][number]) {
    try {
      const dir = await api.get<Node>(`/nodes/${it.dir_ref}`)
      const root = session.roots.find((r) => r.id === dir.root_id)
      const rel = dir.rel_path === '' ? it.name : `${dir.rel_path}/${it.name}`
      const path = root ? await hostPathOf(root, rel) : null
      if (path === null) return toast(t.top.copyUnavailable, 'error')
      await navigator.clipboard.writeText(path)
      toast(t.top.copied, 'saved')
    } catch {
      toast(t.top.copyFailed, 'error')
    }
  }

  $effect(() => {
    const r = ref
    const k = kind
    const mine = ++seq
    result = null
    failed = false
    // The files below the folder and the ones directly in it come as one list; the
    // direct ones are those whose folder is this one.
    api
      .get<TopResult>(`/nodes/${r}/top`, { kind: k, scope: 'both' })
      .then((x) => mine === seq && (result = x))
      .catch(() => mine === seq && (failed = true))
  })
</script>

<div class="controls">
  <div class="seg" role="group" aria-label={t.top.kindLabel}>
    <button aria-pressed={kind === 'largest'} class:on={kind === 'largest'} onclick={() => (kind = 'largest')}>{t.top.largest}</button>
    <button aria-pressed={kind === 'oldest'} class:on={kind === 'oldest'} onclick={() => (kind = 'oldest')}>{t.top.oldest}</button>
  </div>
</div>

{#if failed}
  <EmptyState title={t.common.error} body={t.list.failed} />
{:else if result && !result.stored}
  <EmptyState title={t.tabs.top} body={t.top.notStored}>
    {#if session.canListFiles() && onlist}
      <Button onclick={() => onlist(ref, 'subtree')}>{t.top.listFiles}</Button>
    {/if}
  </EmptyState>
{:else if result && result.items.length === 0}
  <EmptyState title={t.tabs.top} body={t.top.empty} />
{:else if result}
  <ol>
    {#each result.items as it, i (i)}
      {@const here = it.dir_ref === ref}
      <li>
        <span class="name" style="unicode-bidi: isolate" title={displayName(it.name)}><strong>{displayName(it.name)}</strong></span>
        <span class="mono num">{formatBytes(it.size)}</span>
        <span class="mono num">{formatDate(it.mtime)}</span>
        <span class="actions">
          <!-- aria-disabled, not disabled: a disabled button shows no tooltip -->
          <button
            class="icon"
            class:dim={here}
            title={here ? t.top.thisFolder : t.top.openFolder}
            aria-label={here ? t.top.thisFolder : t.top.openFolder}
            aria-disabled={here}
            onclick={() => !here && browse.open(it.dir_ref)}
          >
            <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 6.5A1.5 1.5 0 0 1 4.5 5H9l2 2h8.5A1.5 1.5 0 0 1 21 8.5V18a1.5 1.5 0 0 1-1.5 1.5h-15A1.5 1.5 0 0 1 3 18V6.5Z" /></svg>
          </button>
          <button class="icon" title={t.top.copyPath} aria-label={t.top.copyPath} onclick={() => copyPath(it)}>
            <svg viewBox="0 0 24 24" aria-hidden="true"><rect x="9" y="9" width="11" height="11" rx="2" /><path d="M5 15V6a2 2 0 0 1 2-2h9" /></svg>
          </button>
        </span>
      </li>
    {/each}
  </ol>
{/if}

<style>
  .controls {
    display: flex;
    flex-wrap: wrap;
    column-gap: 8px;
    flex-shrink: 0;
    align-self: flex-start;
  }
  .seg {
    display: inline-flex;
    border: var(--edge);
    border-radius: var(--r-pill);
    overflow: hidden;
    margin: 8px 0;
    flex-shrink: 0;
    align-self: flex-start;
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
    flex: 1 1 auto;
    min-height: 0;
    overflow: auto;
  }
  li {
    display: grid;
    grid-template-columns: minmax(0, 1fr) 70px 90px 50px;
    gap: 8px;
    padding: 5px 4px;
    border-bottom: 1px solid var(--hair);
    align-items: center;
    white-space: nowrap;
  }
  .name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .icon.dim {
    color: var(--ink-3);
    opacity: 0.4;
    cursor: default;
  }
  .icon.dim:hover {
    background: none;
  }
  .num {
    text-align: right;
  }
  .actions {
    display: inline-flex;
    gap: 2px;
  }
  .icon {
    display: inline-flex;
    padding: 3px;
    border: none;
    background: none;
    color: var(--terra);
    cursor: pointer;
    border-radius: 4px;
  }
  .icon:hover,
  .icon:focus-visible {
    background: var(--sunk);
  }
  .icon svg {
    width: 15px;
    height: 15px;
    fill: none;
    stroke: currentColor;
    stroke-width: 1.8;
    stroke-linecap: round;
    stroke-linejoin: round;
  }
</style>
