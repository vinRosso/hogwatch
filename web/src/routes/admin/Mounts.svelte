<script lang="ts">
  import { onMount, untrack } from 'svelte'
  import { api } from '../../lib/api/client'
  import type { components } from '../../lib/api/schema'
  import type { AdminRoot, AvailableMount, RootInput } from '../../lib/api/types'
  import { displayName } from '../../lib/fname'
  import { formatBytes } from '../../lib/format'
  import { problemText } from '../../lib/errors'
  import { t } from '../../lib/i18n/en'
  import ScheduleInput from './ScheduleInput.svelte'
  import { shortCron } from '../../lib/schedule'
  import { navigate, router, setQuery } from '../../lib/router.svelte'
  import { session } from '../../lib/session.svelte'
  import Button from '../../components/ui/Button.svelte'
  import Card from '../../components/ui/Card.svelte'
  import RootName from '../../components/ui/RootName.svelte'
  import Dialog from '../../components/ui/Dialog.svelte'
  import EmptyState from '../../components/ui/EmptyState.svelte'
  import Field from '../../components/ui/Field.svelte'
  import GrowthFigure from '../../components/ui/GrowthFigure.svelte'
  import Pill from '../../components/ui/Pill.svelte'

  type Mount = components['schemas']['MountStat']
  const windows = ['7d', '30d', '90d'] as const
  // The URL wins for a shared link; otherwise the account's window, shared with the Users view.
  const win = $derived(windows.find((w) => w === router.query.window) ?? windows.find((w) => w === session.prefs().admin_window) ?? '30d')
  function pickWindow(w: (typeof windows)[number]) {
    session.setPrefs({ admin_window: w })
    setQuery({ window: undefined }, { replace: true })
  }

  // Root configuration and the mounts the scanner sees: once.
  let roots = $state<AdminRoot[] | null>(null)
  let available = $state<AvailableMount[]>([])
  let rootsFailed = $state(false)
  async function loadRoots() {
    rootsFailed = false
    try {
      const [r, m] = await Promise.all([
        api.get<{ items: AdminRoot[] }>('/admin/roots'),
        api.get<{ items: AvailableMount[] }>('/admin/available-mounts'),
      ])
      roots = r.items
      available = m.items
    } catch {
      rootsFailed = true
    }
  }
  onMount(() => void loadRoots())

  // Space figures: again for each window.
  let stats = $state<Mount[] | null>(null)
  let statsFailed = $state(false)
  let seq = 0
  async function loadStats(w: string) {
    const mine = ++seq
    statsFailed = false
    try {
      const p = await api.get<{ items: Mount[] }>('/admin/mounts', { window: w })
      if (mine === seq) stats = p.items
    } catch {
      if (mine === seq) statsFailed = true
    }
  }
  $effect(() => {
    const w = win
    untrack(() => void loadStats(w))
  })

  const statOf = $derived(new Map((stats ?? []).map((m) => [m.root_id, m])))
  const retry = () => {
    if (rootsFailed) void loadRoots()
    if (statsFailed) void loadStats(win)
  }

  // Donut: share of the filesystem that is allocated to the scan, as a percentage of its total.
  const cx = 52
  const outerR = 50
  const innerR = 34
  const point = (r: number, pct: number) => {
    const a = ((-90 + pct * 3.6) * Math.PI) / 180
    return `${(cx + r * Math.cos(a)).toFixed(2)} ${(cx + r * Math.sin(a)).toFixed(2)}`
  }
  // One band from `from` to `to` percent; a full circle is drawn as 99.99 so the arc has an end point.
  const slice = (from: number, to: number) => {
    const end = Math.min(to, 99.99)
    const large = end - from > 50 ? 1 : 0
    return `M ${point(outerR, from)} A ${outerR} ${outerR} 0 ${large} 1 ${point(outerR, end)} L ${point(innerR, end)} A ${innerR} ${innerR} 0 ${large} 0 ${point(innerR, from)} Z`
  }
  const share = (m: Mount | undefined) => (m && m.bytes_alloc !== null && m.fs_total ? (m.bytes_alloc / m.fs_total) * 100 : null)
  const shareText = (s: number) => (s < 10 ? `${s.toFixed(1)}%` : `${Math.round(s)}%`)

  // New root: only mounts that no root uses yet can become one.
  const free = $derived(available.filter((m) => m.root_id === null))
  let creating = $state(false)
  let pick = $state('')
  let form = $state({ name: '', cross_fs: false, schedule: '', enabled: true, excludes: '' })
  let error = $state<string | null>(null)
  const chosen = $derived(free.find((m) => m.container_path === pick) ?? null)

  function openCreate() {
    creating = true
    error = null
    pick = free[0]?.container_path ?? ''
  }

  async function create(e: Event) {
    e.preventDefault()
    if (!chosen) return
    error = null
    const body: RootInput = {
      name: form.name,
      container_path: chosen.container_path,
      cross_fs: form.cross_fs,
      enabled: form.enabled,
      excludes: form.excludes.split('\n').map((s) => s.trim()).filter((s) => s !== ''),
    }
    if (form.schedule.trim() !== '') body.schedule = form.schedule.trim()
    try {
      const r = await api.post<AdminRoot>('/admin/roots', body)
      creating = false
      navigate(`/admin/roots/${r.id}`)
    } catch (err) {
      error = problemText(err, t.admin.failed)
    }
  }
</script>

<section>
  <header class="top">
    <h1>{t.admin.sections.mounts}</h1>
    <div class="win" role="group" aria-label={t.usage.window}>
      {#each windows as w (w)}
        <button class:on={win === w} aria-pressed={win === w} onclick={() => pickWindow(w)}>{t.history.windows[w]}</button>
      {/each}
    </div>
    <Button onclick={openCreate}>{t.admin.roots.new}</Button>
  </header>

  {#if rootsFailed || statsFailed}
    <EmptyState title={t.common.error} body={rootsFailed ? t.admin.roots.loadFailed : t.admin.mounts.failed}>
      <Button onclick={retry}>{t.common.retry}</Button>
    </EmptyState>
  {:else if roots === null || stats === null}
    <p>{t.common.loading}</p>
  {:else if roots.length === 0}
    <EmptyState title={t.admin.sections.mounts} body={t.admin.roots.empty} />
  {:else}
    <div class="grid">
      {#each roots as r (r.id)}
        {@const m = statOf.get(r.id)}
        {@const pct = share(m)}
        <div class:off={!r.enabled}>
          <Card>
            <div class="head">
              <h2><RootName id={r.id} name={r.name} /></h2>
              <div class="pills">
                {#if r.cross_fs}<Pill>{t.admin.roots.crossFs}</Pill>{/if}
                {#if !r.enabled}<Pill tone="danger">{t.admin.roots.disabled}</Pill>{/if}
              </div>
              <Button variant="ghost" onclick={() => navigate(`/admin/roots/${r.id}`)}>{t.admin.roots.edit}</Button>
            </div>
            <p class="path mono" style="unicode-bidi: isolate" title={t.admin.roots.host}>{displayName(r.host_path)}</p>
            <div class="usage">
              <div class="gauge">
                <svg class="donut" viewBox="0 0 104 104" role="img" aria-label={pct === null ? t.admin.mounts.allocated : `${shareText(pct)} ${t.admin.mounts.used}`}>
                  {#if pct === null || pct < 100}<path class="free" d={slice(pct ?? 0, 100)} />{/if}
                  {#if pct !== null && pct > 0}<path class="used" d={slice(0, pct)} />{/if}
                  <text class="pct" x="52" y="52" dominant-baseline="central">{pct === null ? t.common.none : shareText(pct)}</text>
                </svg>
                {#if m?.fs_total}<span class="total">{t.admin.mounts.total(formatBytes(m.fs_total))}</span>{/if}
              </div>
              <div class="tiles">
                <div class="tile">
                  <span>{t.admin.mounts.allocated}</span>
                  <b class="mono">{m?.bytes_alloc == null ? t.common.none : formatBytes(m.bytes_alloc)}</b>
                </div>
                <div class="tile">
                  <span>{t.admin.mounts.free}</span>
                  <b class="mono">{m?.fs_free == null ? t.common.none : formatBytes(m.fs_free)}</b>
                </div>
                <div class="tile wide">
                  <span>{t.admin.mounts.growth} ({t.history.windows[win]})</span>
                  <GrowthFigure bytes={m?.growth_bytes ?? null} />
                </div>
              </div>
            </div>
            <dl>
              {#if m && m.zfs_used !== null}<dt>{t.admin.mounts.zfsUsed}</dt><dd class="mono">{formatBytes(m.zfs_used)}</dd>{/if}
              {#if m && m.zfs_avail !== null}<dt>{t.admin.mounts.zfsAvail}</dt><dd class="mono">{formatBytes(m.zfs_avail)}</dd>{/if}
              {#if m && m.zfs_quota !== null}<dt>{t.admin.mounts.zfsQuota}</dt><dd class="mono">{formatBytes(m.zfs_quota)}</dd>{/if}
              {#if m && m.zfs_used_snapshots !== null}<dt>{t.admin.mounts.zfsSnap}</dt><dd class="mono">{formatBytes(m.zfs_used_snapshots)}</dd>{/if}
            </dl>
            <dl class="cfg">
              <dt>{t.admin.roots.container}</dt>
              <dd class="mono" style="unicode-bidi: isolate">{displayName(r.container_path)}</dd>
              <dt>{t.admin.roots.schedule}</dt>
              <dd style="white-space: nowrap">{r.schedule ? shortCron(r.schedule) : t.common.none}</dd>
              {#if r.zfs_dataset}<dt>{t.admin.roots.zfs}</dt><dd class="mono">{r.zfs_dataset}</dd>{/if}
              <dt>{t.admin.roots.current}</dt>
              <dd>{r.current_scan_id === null ? t.admin.roots.none : `#${r.current_scan_id}`}</dd>
            </dl>
          </Card>
        </div>
      {/each}
    </div>
  {/if}
</section>

<Dialog open={creating} title={t.admin.roots.new} onclose={() => (creating = false)}>
  {#if free.length === 0}
    <p>{t.admin.roots.mountNone}</p>
  {:else}
    <form onsubmit={create} novalidate>
      <Field label={t.admin.roots.mount}>
        <select bind:value={pick}>
          {#each free as m (m.container_path)}
            <option value={m.container_path}>{displayName(m.container_path)} ({m.fstype})</option>
          {/each}
        </select>
      </Field>
      {#if chosen && !chosen.readonly}<p class="warn" role="alert">{t.admin.roots.writable}</p>{/if}
      <Field label={t.admin.roots.name}><input bind:value={form.name} required autocomplete="off" /></Field>
      <ScheduleInput bind:value={form.schedule} />
      <Field label={t.admin.roots.excludes}><textarea rows="3" bind:value={form.excludes} spellcheck="false"></textarea></Field>
      <label class="chk"><input type="checkbox" bind:checked={form.cross_fs} /> {t.admin.roots.crossFs}</label>
      <label class="chk"><input type="checkbox" bind:checked={form.enabled} /> {t.admin.roots.enabled}</label>
      {#if error}<p class="err" role="alert">{error}</p>{/if}
      <div class="row">
        <Button variant="ghost" type="button" onclick={() => (creating = false)}>{t.common.cancel}</Button>
        <Button type="submit" disabled={!chosen || form.name.trim() === ''}>{t.admin.roots.create}</Button>
      </div>
    </form>
  {/if}
</Dialog>

<style>
  .top {
    display: flex;
    gap: 16px;
    align-items: center;
    margin-bottom: 12px;
    flex-wrap: wrap;
  }
  h1 {
    font: 600 24px var(--serif);
    margin: 0;
    flex: 1;
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
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
    gap: 16px;
  }
  .off :global(.card) {
    background-image: repeating-linear-gradient(-45deg, transparent 0 6px, color-mix(in srgb, var(--ink-2) 14%, transparent) 6px 7px);
  }
  .head {
    display: flex;
    justify-content: space-between;
    align-items: baseline;
    gap: 8px;
  }
  h2 {
    font-size: 16px;
    margin: 0;
  }
  .pills {
    display: flex;
    gap: 4px;
    flex-wrap: wrap;
  }
  .path {
    margin: 2px 0 10px;
    color: var(--ink-2);
    font-size: 12px;
    overflow-wrap: anywhere;
  }
  dl {
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 2px 14px;
    margin: 0 0 10px;
  }
  dl.cfg {
    padding-top: 8px;
    border-top: 1px solid var(--hair);
    font-size: 13px;
  }
  .usage {
    display: flex;
    gap: 16px;
    align-items: center;
    flex-wrap: wrap;
    margin: 0 0 12px;
  }
  .gauge {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 4px;
    flex: none;
  }
  .donut {
    width: 80px;
    height: 80px;
  }
  .donut .free {
    fill: var(--hair);
  }
  .donut .used {
    fill: var(--ink-2);
  }
  .donut text {
    text-anchor: middle;
    fill: var(--ink-2);
  }
  .donut .pct {
    font-size: 18px;
    font-weight: 500;
    fill: var(--ink);
  }
  .total {
    font-size: 13px;
    color: var(--ink-2);
    white-space: nowrap;
  }
  .tiles {
    flex: 1;
    min-width: 0;
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 8px;
  }
  .tile {
    background: var(--paper);
    border-radius: 8px;
    padding: 8px;
    min-width: 0;
  }
  .tile span {
    display: block;
    color: var(--ink-2);
    font-size: 12px;
  }
  .tile b {
    font-size: 14px;
    font-weight: 500;
    white-space: nowrap;
  }
  .tile.wide {
    grid-column: 1 / -1;
  }
  dt {
    color: var(--ink-2);
  }
  dd {
    margin: 0;
    text-align: right;
    overflow-wrap: anywhere;
  }
  .warn,
  .err {
    color: var(--danger);
  }
  .chk {
    display: block;
    margin-bottom: 8px;
  }
  .row {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
  }
</style>
