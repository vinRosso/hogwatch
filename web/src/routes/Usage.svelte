<script lang="ts">
  import { untrack } from 'svelte'
  import { api } from '../lib/api/client'
  import type { MyUsage } from '../lib/api/types'
  import { displayName } from '../lib/fname'
  import { formatBytes } from '../lib/format'
  import { t } from '../lib/i18n/en'
  import { rootColor } from '../lib/rootColor'
  import { router, setQuery } from '../lib/router.svelte'
  import { session } from '../lib/session.svelte'
  import { share, sortPaths, type PathSort } from '../lib/usageView'
  import QuotaBar from '../components/shell/QuotaBar.svelte'
  import Card from '../components/ui/Card.svelte'
  import Donut, { partsOf } from '../components/ui/Donut.svelte'
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

  let by = $state<PathSort>('size')
  const sorts: PathSort[] = ['size', 'name']
  const sortLabel = { size: t.usage.sortSize, name: t.usage.sortName }

  const total = $derived(usage?.total.bytes_alloc ?? 0)
  const pct = (bytes: number) => `${Math.round(share(bytes, total) * 100)} %`
  const folders = $derived(usage ? sortPaths(usage.paths, by, (p) => displayName(p.host_path)) : [])
  // One root is a full ring and says nothing a bar does not; the donut is for comparing roots.
  const parts = $derived(
    usage && usage.mounts.length > 1
      ? partsOf(
          usage.mounts.map((m) => ({
            name: displayName(m.root_name),
            color: rootColor(m.root_id),
            bytes: m.usage.bytes_alloc,
            gauge:
              m.fs_total !== null && m.fs_free !== null && m.fs_total > 0
                ? { fraction: (m.fs_total - m.fs_free) / m.fs_total, limit: formatBytes(m.fs_total) }
                : undefined,
          })),
          t.usage.other,
        )
      : [],
  )
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
      <div class="hero" class:split={parts.length > 0}>
        <div class="main">
          <QuotaBar big />
          <dl class="tiles">
            <div class="tile">
              <dt>{t.usage.allocated}</dt>
              <dd class="mono">{formatBytes(usage.total.bytes_alloc)}</dd>
            </div>
            <div class="tile">
              <dt>{t.usage.growth} ({t.history.windows[win]})</dt>
              <dd><GrowthFigure bytes={usage.growth_bytes} /></dd>
            </div>
            <div class="tile">
              <dt>{t.usage.files}</dt>
              <dd class="mono">{usage.total.files.toLocaleString()}</dd>
            </div>
          </dl>
        </div>
        {#if parts.length > 0}
          <Donut {parts} caption={t.usage.allocated} label={t.usage.chart} />
        {/if}
      </div>
    </Card>

    <Card title={t.usage.mounts}>
      <ul class="list">
        {#each usage.mounts as m (m.root_id)}
          <li>
            <div class="line">
              <strong class="p"><RootName id={m.root_id} name={m.root_name} /></strong>
              <span class="mono">{formatBytes(m.usage.bytes_alloc)}</span>
              <GrowthFigure bytes={m.growth_bytes} />
            </div>
            <div class="bar" role="img" aria-label={t.usage.share(pct(m.usage.bytes_alloc))}>
              <span style:width={`${share(m.usage.bytes_alloc, total) * 100}%`} style:--fill={rootColor(m.root_id)}></span>
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
      {#snippet actions()}
        <div class="win" role="group" aria-label={t.usage.sort}>
          {#each sorts as s (s)}
            <button class:on={by === s} aria-pressed={by === s} onclick={() => (by = s)}>{sortLabel[s]}</button>
          {/each}
        </div>
      {/snippet}
      <ul class="list">
        {#each folders as p (p.user_path_id)}
          <li>
            <div class="line">
              <span class="p" style="unicode-bidi: isolate" title={displayName(p.host_path)}>{displayName(p.host_path)}</span>
              <span class="root"><RootName id={p.root_id} name={rootName(p.root_id) ?? ''} /></span>
              <span class="mono">{formatBytes(p.usage.bytes_alloc)}</span>
              {#if !p.found}<Pill tone="warn">{t.usage.notFound}</Pill>{/if}
              {#if p.redundant}<Pill>{t.usage.redundant}</Pill>{/if}
            </div>
            <div class="bar" role="img" aria-label={t.usage.share(pct(p.usage.bytes_alloc))}>
              <span style:width={`${share(p.usage.bytes_alloc, total) * 100}%`} style:--fill={rootColor(p.root_id)}></span>
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
    margin-inline: auto;
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
  .hero {
    display: grid;
    gap: 16px;
  }
  .hero.split {
    grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
    align-items: center;
  }
  @media (max-width: 720px) {
    .hero.split {
      grid-template-columns: minmax(0, 1fr);
    }
  }
  .tiles {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    margin: 12px 0 0;
  }
  .tile {
    flex: 1 1 120px;
    padding: 8px 12px;
    background: var(--sunk);
    border-radius: var(--r);
  }
  dt {
    color: var(--ink-2);
    font-size: 12px;
  }
  dd {
    margin: 2px 0 0;
    font-size: 16px;
    font-weight: 600;
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  li {
    padding: 8px 0;
    border-bottom: 1px solid var(--hair);
    display: grid;
    gap: 4px;
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
  .bar {
    height: 6px;
    border-radius: 3px;
    background: var(--sunk);
    overflow: hidden;
  }
  .bar span {
    display: block;
    height: 100%;
    min-width: 2px;
    background: var(--fill);
  }
  .root,
  .sub {
    color: var(--ink-2);
    font-size: 13px;
  }
</style>
