<script lang="ts">
  import { onDestroy, onMount, tick } from 'svelte'
  import { api, ApiError } from '../../lib/api/client'
  import type { AdminRoot, ScanAdmin, ScanJob } from '../../lib/api/types'
  import { displayName } from '../../lib/fname'
  import { formatBytes, formatCount, formatDateTime, formatDuration } from '../../lib/format'
  import { problemText } from '../../lib/errors'
  import { t } from '../../lib/i18n/en'
  import { shortCron } from '../../lib/schedule'
  import Button from '../../components/ui/Button.svelte'
  import Card from '../../components/ui/Card.svelte'
  import Dialog from '../../components/ui/Dialog.svelte'
  import EmptyState from '../../components/ui/EmptyState.svelte'
  import Pill from '../../components/ui/Pill.svelte'
  import RootName from '../../components/ui/RootName.svelte'

  let roots = $state<AdminRoot[]>([])
  let scans = $state<ScanAdmin[]>([])
  // cursors[i] opens page i (the first page has none); the next one is known once page i has loaded.
  let cursors = $state<(string | null)[]>([null])
  let page = $state(0)
  let pageSize = 0
  let wrap: HTMLDivElement | undefined = $state()
  let errorOf = $state<ScanAdmin | null>(null)
  let loaded = $state(false)
  let failed = $state(false)
  let error = $state<string | null>(null)
  // Running jobs by root id; each polls until a terminal status.
  let jobs = $state<Record<number, ScanJob>>({})
  const timers = new Map<number, ReturnType<typeof setTimeout>>()
  let gone = false

  const terminal = (s: string) => s === 'done' || s === 'failed' || s === 'cancelled'
  const tone = (s: string) => (s === 'failed' ? 'danger' : s === 'done' ? 'ok' : s === 'running' || s === 'queued' ? 'warn' : 'neutral')
  const rootName = (id: number) => roots.find((r) => r.id === id)?.name ?? String(id)

  const ROW = 33
  const PAGER = 52

  // Rows that fit between the top of the table and the bottom of the window, at least a few.
  function fit(): number {
    if (!wrap) return 10
    const top = wrap.getBoundingClientRect().top
    const head = wrap.querySelector('thead')?.getBoundingClientRect().height ?? ROW
    const row = wrap.querySelector('tbody tr')?.getBoundingClientRect().height || ROW
    return Math.min(100, Math.max(5, Math.floor((window.innerHeight - top - head - PAGER - 16) / row)))
  }

  let latest = 0
  async function loadScans(i = page) {
    const mine = ++latest
    try {
      const p = await api.get<{ items: ScanAdmin[]; next_cursor: string | null }>('/admin/scans', { cursor: cursors[i] ?? undefined, limit: pageSize })
      if (mine !== latest) return
      scans = p.items
      page = i
      cursors = [...cursors.slice(0, i + 1), ...(p.next_cursor ? [p.next_cursor] : [])]
      failed = false
      loaded = true
      // A scan already running (started elsewhere, or before a reload) is tracked and can be cancelled.
      for (const s of p.items) {
        if (s.job_id !== null && (s.status === 'running' || s.status === 'queued') && !jobs[s.root_id] && !seen.has(s.job_id)) track(s.root_id, s.job_id, s.status)
      }
    } catch {
      if (mine === latest) failed = true
    }
  }

  let resizing: ReturnType<typeof setTimeout> | undefined
  function onResize() {
    clearTimeout(resizing)
    resizing = setTimeout(() => {
      const n = fit()
      if (n === pageSize) return
      pageSize = n
      cursors = [null]
      void loadScans(0)
    }, 150)
  }

  onMount(async () => {
    try {
      roots = (await api.get<{ items: AdminRoot[] }>('/admin/roots')).items
    } catch {
      failed = true
    }
    await tick()
    pageSize = fit()
    await loadScans(0)
  })

  async function poll(rootId: number, jobId: number) {
    if (gone) return
    try {
      const j = await api.get<ScanJob>(`/scans/${jobId}/progress`)
      if (gone) return
      jobs[rootId] = j
      if (terminal(j.status)) {
        timers.delete(rootId)
        delete jobs[rootId]
        await loadScans()
        return
      }
    } catch (e) {
      // A refusal (gone, signed out) will not get better; a network error might.
      if (e instanceof ApiError && e.status >= 400 && e.status < 500) {
        timers.delete(rootId)
        delete jobs[rootId]
        return
      }
    }
    timers.set(rootId, setTimeout(() => void poll(rootId, jobId), 2000))
  }

  const starting = new Set<number>()
  const seen = new Set<number>()
  function track(rootId: number, jobId: number, status: string) {
    seen.add(jobId)
    jobs[rootId] = { job_id: jobId, root_id: rootId, status: status as ScanJob['status'], scan_id: null, progress: null, error: null, created_at: 0, started_at: null, finished_at: null }
    void poll(rootId, jobId)
  }

  async function scanNow(rootId: number) {
    if (starting.has(rootId)) return
    starting.add(rootId)
    error = null
    try {
      const r = await api.post<{ id: number }>('/scans', { root_id: rootId })
      track(rootId, r.id, 'queued')
    } catch (err) {
      error = problemText(err, t.admin.failed)
    } finally {
      starting.delete(rootId)
    }
  }

  async function cancel(rootId: number) {
    const j = jobs[rootId]
    if (!j) return
    try {
      await api.post(`/scans/${j.job_id}/cancel`)
    } catch (err) {
      error = problemText(err, t.admin.failed)
    }
  }

  onDestroy(() => {
    gone = true
    clearTimeout(resizing)
    for (const h of timers.values()) clearTimeout(h)
  })
</script>

<section>
  <h1>{t.admin.sections.scans}</h1>
  {#if error}<p class="err" role="alert">{error}</p>{/if}

  <div class="roots">
    {#each roots as r (r.id)}
      <Card>
        <div class="head">
          <h2><RootName id={r.id} name={r.name} /></h2>
          {#if jobs[r.id]}
            <Button variant="ghost" onclick={() => cancel(r.id)}>{t.admin.scans.cancel}</Button>
          {:else}
            <Button variant="ghost" onclick={() => scanNow(r.id)}>{t.admin.scans.scanNow}</Button>
          {/if}
        </div>
        <div class="foot">
          <span class="sched">{shortCron(r.schedule)}</span>
          {#if jobs[r.id]}<Pill tone="warn">{t.listing.status[jobs[r.id].status] ?? jobs[r.id].status}</Pill>{/if}
        </div>
      </Card>
    {/each}
  </div>

  {#if failed}
    <EmptyState title={t.common.error} body={t.admin.scans.loadFailed}>
      <Button onclick={() => loadScans()}>{t.common.retry}</Button>
    </EmptyState>
  {:else if loaded && scans.length === 0 && page === 0}
    <EmptyState title={t.admin.sections.scans} body={t.admin.scans.empty} />
  {:else}
    <div class="scroll" bind:this={wrap}>
      <table>
        <thead>
          <tr>
            <th>{t.admin.scans.root}</th><th>{t.admin.scans.status}</th><th>{t.admin.scans.trigger}</th><th>{t.admin.scans.started}</th>
            <th class="r">{t.admin.scans.duration}</th><th class="r">{t.admin.scans.dirs}</th><th class="r">{t.admin.scans.files}</th>
            <th class="r">{t.admin.scans.bytes}</th><th class="r">{t.admin.scans.errors}</th>
          </tr>
        </thead>
        <tbody>
          {#each scans as s (s.id)}
            <tr>
              <td><RootName id={s.root_id} name={rootName(s.root_id)} /></td>
              <td><Pill tone={tone(s.status)}>{t.listing.status[s.status] ?? s.status}</Pill></td>
              <td>{s.trigger}</td>
              <td class="mono">{formatDateTime(s.started_at)}</td>
              <td class="mono r">{s.finished_at === null ? t.common.none : formatDuration(s.finished_at - s.started_at)}</td>
              <td class="mono r">{formatCount(s.dirs)}</td>
              <td class="mono r">{formatCount(s.files)}</td>
              <td class="mono r">{formatBytes(s.bytes_alloc)}</td>
              <td class="mono r">
                {#if s.error_count > 0 || s.error}
                  <button class="errbtn" aria-label={t.admin.scans.showErrors(s.error_count)} onclick={() => (errorOf = s)}>
                    <svg viewBox="0 0 16 16" aria-hidden="true"><path d="M8 1.5l7 12.5H1z" /><path d="M8 6v4M8 11.8v.4" /></svg>
                    {formatCount(s.error_count)}
                  </button>
                {:else}
                  {formatCount(0)}
                {/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
    {#if loaded}
      <nav class="pager" aria-label={t.admin.sections.scans}>
        <Button variant="ghost" disabled={page === 0} onclick={() => loadScans(page - 1)}>{t.admin.scans.prev}</Button>
        <span>{t.admin.scans.page(page + 1)}</span>
        <Button variant="ghost" disabled={cursors.length <= page + 1} onclick={() => loadScans(page + 1)}>{t.admin.scans.next}</Button>
      </nav>
    {/if}
  {/if}
</section>

<svelte:window onresize={onResize} />

<Dialog open={errorOf !== null} title={t.admin.scans.errorTitle} onclose={() => (errorOf = null)}>
  {#if errorOf}
    {#if errorOf.error}<p class="errmsg" style="unicode-bidi: isolate">{displayName(errorOf.error)}</p>{/if}
    {#if errorOf.error_count > 0}<p>{t.admin.scans.unreadable(errorOf.error_count)}</p>{/if}
  {/if}
</Dialog>

<style>
  h1 {
    font: 600 24px var(--serif);
    margin: 0 0 12px;
  }
  .roots {
    margin: 0 0 16px;
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
    gap: 12px;
  }
  .head {
    display: flex;
    justify-content: space-between;
    align-items: baseline;
    gap: 8px;
  }
  .roots h2 {
    font-size: 16px;
    margin: 0;
    overflow-wrap: anywhere;
  }
  .foot {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 8px;
    margin-top: 10px;
  }
  .sched {
    color: var(--ink-2);
    font-size: 13px;
  }
  .scroll {
    overflow-x: auto;
  }
  table {
    width: 100%;
    border-collapse: collapse;
  }
  th,
  td {
    text-align: left;
    padding: 5px 8px;
    border-bottom: 1px solid var(--hair);
    vertical-align: top;
  }
  th {
    color: var(--ink-2);
    font-weight: 500;
    font-size: 12px;
  }
  .r {
    text-align: right;
  }
  td {
    height: 33px;
    box-sizing: border-box;
    white-space: nowrap;
    vertical-align: middle;
  }
  .err,
  .errmsg {
    color: var(--danger);
  }
  .errmsg {
    overflow-wrap: anywhere;
  }
  .errbtn {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    border: none;
    background: none;
    padding: 0;
    font: inherit;
    color: var(--danger);
    cursor: pointer;
  }
  .errbtn svg {
    width: 14px;
    height: 14px;
    fill: none;
    stroke: currentColor;
    stroke-width: 1.5;
    stroke-linejoin: round;
    stroke-linecap: round;
  }
  .pager {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 12px;
    padding-top: 10px;
    color: var(--ink-2);
    font-size: 13px;
  }
</style>
