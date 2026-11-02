<script lang="ts">
  import { untrack } from 'svelte'
  import { api } from '../lib/api/client'
  import type { MyUsage } from '../lib/api/types'
  import { displayName } from '../lib/fname'
  import { formatBytes } from '../lib/format'
  import { t } from '../lib/i18n/en'
  import { router, setQuery } from '../lib/router.svelte'
  import { session } from '../lib/session.svelte'
  import QuotaBar from '../components/shell/QuotaBar.svelte'
  import Card from '../components/ui/Card.svelte'
  import EmptyState from '../components/ui/EmptyState.svelte'
  import GrowthFigure from '../components/ui/GrowthFigure.svelte'
  import Pill from '../components/ui/Pill.svelte'
  import RootName from '../components/ui/RootName.svelte'
  import Button from '../components/ui/Button.svelte'

  const windows = ['7d', '30d', '90d'] as const
  // The URL wins for a shared link; otherwise the account's window.
  const win = $derived(windows.find((w) => w === router.query.window) ?? windows.find((w) => w === session.prefs().usage_window) ?? '30d')
  function pick(w: (typeof windows)[number]) {
    session.setPrefs({ usage_window: w })
    setQuery({ window: undefined }, { replace: true })
  }

  let usage = $state<MyUsage | null>(null)
  let failed = $state(false)
  let seq = 0

  async function load(w: string) {
    const mine = ++seq
    failed = false
    try {
      const u = await api.get<MyUsage>('/me/usage', { window: w })
      if (mine === seq) usage = u
    } catch {
      if (mine === seq) failed = true
    }
  }

  $effect(() => {
    const w = win
    untrack(() => void load(w))
  })

  const rootName = (id: number) => session.roots.find((r) => r.id === id)?.name
</script>

<section class="page">
  <header class="top">
    <h1>{t.usage.title}</h1>
    <div class="win" role="group" aria-label={t.usage.window}>
      {#each windows as w (w)}
        <button class:on={win === w} aria-pressed={win === w} onclick={() => pick(w)}>
          {t.history.windows[w]}
        </button>
      {/each}
    </div>
  </header>

  {#if failed}
    <EmptyState title={t.common.error} body={t.usage.failed}>
      <Button onclick={() => load(win)}>{t.common.retry}</Button>
    </EmptyState>
  {:else if !usage}
    <p>{t.common.loading}</p>
  {:else if usage.paths.length === 0 && usage.mounts.length === 0}
    <EmptyState title={t.usage.title} body={t.usage.empty} />
  {:else}
    <Card title={t.usage.total}>
      <QuotaBar big />
      <dl class="tot">
        <dt>{t.usage.allocated}</dt>
        <dd class="mono">{formatBytes(usage.total.bytes_alloc)}</dd>
        <dt>{t.usage.growth} ({t.history.windows[win]})</dt>
        <dd><GrowthFigure bytes={usage.growth_bytes} /></dd>
      </dl>
    </Card>

    <Card title={t.usage.mounts}>
      <ul class="list">
        {#each usage.mounts as m (m.root_id)}
          <li>
            <div class="line">
              <strong><RootName id={m.root_id} name={m.root_name} /></strong>
              <span class="mono">{formatBytes(m.usage.bytes_alloc)}</span>
              <GrowthFigure bytes={m.growth_bytes} />
            </div>
            {#if m.fs_total !== null && m.fs_free !== null}
              <div class="sub">{t.usage.fs(formatBytes(m.fs_free), formatBytes(m.fs_total))}</div>
            {/if}
            {#if m.shared_hardlink_bytes > 0}
              <div class="sub">{t.usage.hardlinks(formatBytes(m.shared_hardlink_bytes))}</div>
            {/if}
          </li>
        {/each}
      </ul>
    </Card>

    <Card title={t.usage.paths}>
      <ul class="list">
        {#each usage.paths as p (p.user_path_id)}
          <li>
            <div class="line">
              <span class="p" style="unicode-bidi: isolate" title={displayName(p.host_path)}>{displayName(p.host_path)}</span>
              <span class="root"><RootName id={p.root_id} name={rootName(p.root_id) ?? ''} /></span>
              <span class="mono">{formatBytes(p.usage.bytes_alloc)}</span>
              {#if !p.found}<Pill tone="warn">{t.usage.notFound}</Pill>{/if}
              {#if p.redundant}<Pill>{t.usage.redundant}</Pill>{/if}
            </div>
          </li>
        {/each}
      </ul>
    </Card>
  {/if}
</section>

<style>
  .page {
    padding: 16px 20px;
    display: grid;
    gap: 16px;
    max-width: 960px;
  }
  .top {
    display: flex;
    align-items: center;
    justify-content: space-between;
  }
  h1 {
    font: 600 24px var(--serif);
    margin: 0;
  }
  .win {
    display: flex;
    border: var(--edge);
    border-radius: var(--r-pill);
    overflow: hidden;
  }
  .win button {
    border: none;
    background: var(--card);
    padding: 3px 12px;
    cursor: pointer;
  }
  .win button.on {
    background: var(--ink);
    color: var(--paper);
  }
  .tot {
    display: grid;
    grid-template-columns: auto auto;
    justify-content: start;
    gap: 2px 18px;
    margin: 12px 0 0;
  }
  dd {
    margin: 0;
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  li {
    padding: 6px 0;
    border-bottom: 1px solid var(--hair);
  }
  .line {
    display: flex;
    gap: 12px;
    align-items: center;
    flex-wrap: wrap;
  }
  .p {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .root,
  .sub {
    color: var(--ink-2);
    font-size: 13px;
  }
</style>
