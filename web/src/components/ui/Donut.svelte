<script module lang="ts">
  /** A bar under a legend row: how full the item is, and the amount it fills (quota or disk size). */
  export type Gauge = { fraction: number; limit: string }
  export type Part = { name: string; bytes: number; color: string; gauge?: Gauge }

  // Three categorical slots pass the palette validator; the rest fold into a neutral "Other".
  const SLOTS = ['var(--terra)', 'var(--mustard)', 'var(--plum)']

  /** The largest items as donut parts, the rest folded into one "Other" part. */
  export function partsOf(items: { name: string; bytes: number; gauge?: Gauge; color?: string }[], other: string): Part[] {
    const rows = items.filter((i) => i.bytes > 0).sort((a, b) => b.bytes - a.bytes)
    const rest = rows.slice(SLOTS.length).reduce((sum, i) => sum + i.bytes, 0)
    return [
      ...rows.slice(0, SLOTS.length).map((i, k) => ({ name: i.name, bytes: i.bytes, color: i.color ?? SLOTS[k], gauge: i.gauge })),
      ...(rest > 0 ? [{ name: other, bytes: rest, color: 'var(--ink-3)' }] : []),
    ]
  }
</script>

<script lang="ts">
  import { formatBytes } from '../../lib/format'

  let { parts, caption, label }: { parts: Part[]; caption: string; label: string } = $props()

  // The popup follows the pointer; it shows at once, with no browser tooltip delay.
  let wrap: HTMLDivElement | undefined = $state()
  let hot = $state<number | null>(null)
  let pos = $state({ x: 0, y: 0 })
  function track(e: PointerEvent, i: number) {
    const box = wrap!.getBoundingClientRect()
    hot = i
    pos = { x: e.clientX - box.left, y: e.clientY - box.top }
  }

  const CX = 60
  const CY = 60
  const R = 54
  const r = 34

  const total = $derived(parts.reduce((sum, p) => sum + p.bytes, 0))
  const slices = $derived.by(() => {
    let angle = -Math.PI / 2
    return parts.map((p) => {
      const share = p.bytes / total
      const start = angle
      angle += share * 2 * Math.PI
      return { ...p, share, start, end: angle }
    })
  })

  const point = (a: number, rad: number) => `${(CX + rad * Math.cos(a)).toFixed(2)} ${(CY + rad * Math.sin(a)).toFixed(2)}`
  // One ring segment; a full ring is two half arcs, since an arc cannot start and end at one point.
  function ring(s: number, e: number): string {
    const large = e - s > Math.PI ? 1 : 0
    return `M${point(s, R)} A${R} ${R} 0 ${large} 1 ${point(e, R)} L${point(e, r)} A${r} ${r} 0 ${large} 0 ${point(s, r)} Z`
  }
  const arc = (sl: { share: number; start: number; end: number }) =>
    sl.share >= 0.9999 ? ring(sl.start, sl.start + Math.PI) + ring(sl.start + Math.PI, sl.end) : ring(sl.start, sl.end)
  const pct = (share: number) => `${(share * 100).toFixed(1)} %`
</script>

<div class="donut" bind:this={wrap}>
  <svg viewBox="0 0 120 120" role="img" aria-label={label}>
    {#each slices as sl, i (i)}
      <path
        role="img"
        aria-label={`${sl.name}: ${formatBytes(sl.bytes)} (${pct(sl.share)})`}
        d={arc(sl)}
        fill={sl.color}
        stroke="var(--card)"
        stroke-width="2"
        onpointerenter={(e) => track(e, i)}
        onpointermove={(e) => track(e, i)}
        onpointerleave={() => (hot = null)}
      />
    {/each}
    <text x={CX} y="58" text-anchor="middle" class="total">{formatBytes(total)}</text>
    <text x={CX} y="72" text-anchor="middle" class="cap">{caption}</text>
  </svg>
  <ul class="legend">
    {#each slices as sl, i (i)}
      <li>
        <span class="row">
          <span class="sw" style:background={sl.color}></span>
          <span class="name" style="unicode-bidi: isolate">{sl.name}</span>
          <span class="mono">{formatBytes(sl.bytes)}{#if sl.gauge}<span class="limit"> / {sl.gauge.limit}</span>{/if}</span>
        </span>
        {#if sl.gauge}
          <span class="gauge" role="img" aria-label={`${Math.round(sl.gauge.fraction * 100)} %`}>
            <span class="fill" class:over={sl.gauge.fraction > 1} style:width={`${Math.min(sl.gauge.fraction, 1) * 100}%`} style:--fill={sl.color}></span>
          </span>
        {/if}
      </li>
    {/each}
  </ul>
  {#if hot !== null}
    {@const sl = slices[hot]}
    <div class="tip" style:left={`${pos.x + 12}px`} style:top={`${pos.y + 12}px`}>
      <strong>{sl.name}</strong>
      <span class="mono">{formatBytes(sl.bytes)} · {pct(sl.share)}</span>
    </div>
  {/if}
</div>

<style>
  .donut {
    position: relative;
    display: flex;
    align-items: center;
    gap: 16px;
  }
  .tip {
    position: absolute;
    z-index: 2;
    display: grid;
    gap: 2px;
    padding: 6px 8px;
    pointer-events: none;
    white-space: nowrap;
    background: var(--card);
    border: var(--edge);
    border-radius: var(--r);
    box-shadow: var(--pop);
    font-size: 12px;
  }
  svg {
    flex: 0 0 140px;
    width: 140px;
    height: 140px;
  }
  .total {
    font-size: 13px;
    font-weight: 600;
    fill: var(--ink);
  }
  .cap {
    font-size: 9px;
    fill: var(--ink-2);
  }
  .legend {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: 8px;
    min-width: 0;
    flex: 1;
  }
  .legend li {
    display: grid;
    gap: 3px;
    font-size: 13px;
  }
  .row {
    display: grid;
    grid-template-columns: 12px minmax(0, 1fr) auto;
    gap: 8px;
    align-items: center;
  }
  .limit {
    color: var(--ink-2);
  }
  .sw {
    width: 12px;
    height: 12px;
    border-radius: 3px;
  }
  .name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .gauge {
    display: block;
    height: 6px;
    border-radius: 3px;
    background: var(--sunk);
    overflow: hidden;
  }
  .fill {
    display: block;
    height: 100%;
    background: var(--fill);
  }
  /* Over quota: the owner's color with soft stripes, the same ink at a quarter strength as the treemap hatching. */
  .fill.over {
    background: repeating-linear-gradient(
      -45deg,
      var(--fill) 0 4px,
      color-mix(in srgb, var(--ink) 25%, var(--fill)) 4px 6px
    );
  }
</style>
