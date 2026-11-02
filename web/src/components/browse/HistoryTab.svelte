<script lang="ts">
  import uPlot from 'uplot'
  import 'uplot/dist/uPlot.min.css'
  import { onMount } from 'svelte'
  import { api } from '../../lib/api/client'
  import type { HistoryPoint } from '../../lib/api/types'
  import { formatBytes } from '../../lib/format'
  import { t } from '../../lib/i18n/en'
  import { session } from '../../lib/session.svelte'
  import EmptyState from '../ui/EmptyState.svelte'

  let { ref }: { ref: string } = $props()

  const windows = ['7d', '30d', '90d', '1y', 'all'] as const
  const win = $derived(windows.find((w) => w === session.prefs().history_window) ?? '30d')
  let points = $state<HistoryPoint[] | null>(null)
  let failed = $state(false)
  let host: HTMLDivElement | undefined = $state()
  let plot: uPlot | null = null
  let seq = 0
  // The folder the shown points belong to: a window change keeps them while the next set loads,
  // another folder's points are dropped at once.
  let pointsRef = ''

  $effect(() => {
    const mine = ++seq
    if (ref !== pointsRef) {
      pointsRef = ref
      points = null
    }
    failed = false
    api
      .get<{ items: HistoryPoint[] }>(`/nodes/${ref}/history`, { window: win })
      .then((x) => mine === seq && (points = x.items))
      .catch(() => mine === seq && (failed = true))
  })

  function css(name: string) {
    return getComputedStyle(document.documentElement).getPropertyValue(name).trim()
  }

  const SAMPLES = 120
  const DURATION = 350
  let frame = 0
  let animating = false
  // what the plot currently shows, so an interrupted morph restarts from the visible frame
  let shown: [number[], number[]] = [[], []]

  function resample(xs: number[], ys: number[]): [number[], number[]] {
    const ox: number[] = []
    const oy: number[] = []
    const last = xs.length - 1
    for (let i = 0; i < SAMPLES; i++) {
      const f = (i / (SAMPLES - 1)) * last
      const lo = Math.floor(f)
      const hi = Math.min(lo + 1, last)
      const k = f - lo
      ox.push(xs[lo] + (xs[hi] - xs[lo]) * k)
      oy.push(ys[lo] + (ys[hi] - ys[lo]) * k)
    }
    return [ox, oy]
  }

  function morph(u: uPlot, to: [number[], number[]]) {
    cancelAnimationFrame(frame)
    const from = resample(shown[0], shown[1])
    const target = resample(to[0], to[1])
    const start = performance.now()
    animating = true
    const step = (now: number) => {
      const p = Math.min((now - start) / DURATION, 1)
      const e = 1 - (1 - p) ** 3
      if (p < 1) {
        shown = [
          from[0].map((v, i) => v + (target[0][i] - v) * e),
          from[1].map((v, i) => v + (target[1][i] - v) * e),
        ]
        u.setData(shown)
        frame = requestAnimationFrame(step)
      } else {
        animating = false
        shown = to
        u.setData(to)
      }
    }
    frame = requestAnimationFrame(step)
  }

  $effect(() => {
    if (!host || !points || points.length === 0) {
      cancelAnimationFrame(frame)
      animating = false
      plot?.destroy()
      plot = null
      return
    }
    const data: [number[], number[]] = [points.map((p) => p.ts), points.map((p) => p.bytes_alloc)]
    if (plot) {
      if (matchMedia('(prefers-reduced-motion: reduce)').matches) {
        shown = data
        plot.setData(data)
      } else {
        morph(plot, data)
      }
      return
    }
    shown = data
    const ink = css('--ink')
    plot = new uPlot(
      {
        width: host.clientWidth || 380,
        height: 200,
        legend: { show: false },
        cursor: { drag: { x: false, y: false } },
        scales: { x: { time: true } },
        axes: [
          { stroke: ink, font: '11px "IBM Plex Mono", monospace', grid: { stroke: css('--hair'), width: 1 } },
          {
            stroke: ink,
            size: 62,
            font: '11px "IBM Plex Mono", monospace',
            values: (_u, vals) => vals.map((v) => formatBytes(v)),
            grid: { stroke: css('--hair'), width: 1 },
          },
        ],
        series: [{}, { stroke: ink, width: 2, fill: css('--terra-bg'), points: { show: () => !animating && shown[0].length < 40 } }],
      },
      data,
      host,
    )
  })

  onMount(() => {
    const ro = new ResizeObserver(() => {
      if (plot && host && host.clientWidth) plot.setSize({ width: host.clientWidth, height: 200 })
    })
    if (host) ro.observe(host)
    return () => {
      cancelAnimationFrame(frame)
      ro.disconnect()
      plot?.destroy()
    }
  })
</script>

<div class="seg" role="group" aria-label={t.history.window}>
  {#each windows as w (w)}
    <button class:on={win === w} aria-pressed={win === w} onclick={() => session.setPrefs({ history_window: w })}>{t.history.windows[w]}</button>
  {/each}
</div>

{#if failed}
  <EmptyState title={t.common.error} body={t.history.failed} />
{:else if points && points.length === 0}
  <EmptyState title={t.tabs.history} body={t.history.empty} />
{/if}
<div class="chart" bind:this={host} role="img" aria-label={t.history.chart} hidden={failed || !points || points.length === 0}></div>

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
    padding: 3px 10px;
    cursor: pointer;
  }
  .seg button.on {
    background: var(--ink);
    color: var(--paper);
  }
  .chart {
    width: 100%;
  }
</style>
