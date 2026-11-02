<script lang="ts">
  import type { Snippet } from 'svelte'
  import { api } from '../../lib/api/client'
  import { browse, crumbs, DEPTHS, type Depth } from '../../lib/browse.svelte'
  import { t } from '../../lib/i18n/en'
  import FilterBar from './FilterBar.svelte'
  import RootPicker from './RootPicker.svelte'

  let { actions }: { actions?: Snippet } = $props()

  const trail = $derived(
    browse.node && browse.root ? crumbs(browse.node.rel_path, browse.root.anchors, browse.root.name) : [],
  )

  async function go(rel: string) {
    if (!browse.root) return
    const r = await api.get<{ ref: string }>('/resolve', { root: browse.root.id, path: rel })
    browse.open(r.ref)
  }

  // Detail meter: a drag previews locally and commits once on release, so the
  // treemap reloads a single time instead of at every bar crossed.
  let meter: HTMLElement
  let drag = $state<Depth | null>(null)
  const shown = $derived(drag ?? browse.depth)

  function levelAt(x: number): Depth {
    const r = meter.getBoundingClientRect()
    const i = Math.floor(((x - r.left) / r.width) * DEPTHS.length)
    return DEPTHS[Math.min(DEPTHS.length - 1, Math.max(0, i))]
  }

  function commit(d: Depth) {
    if (d !== browse.depth) browse.setDepth(d)
  }

  function down(e: PointerEvent) {
    if (e.button !== 0) return
    meter.setPointerCapture(e.pointerId)
    drag = levelAt(e.clientX)
  }

  function move(e: PointerEvent) {
    if (drag !== null) drag = levelAt(e.clientX)
  }

  function release() {
    if (drag === null) return
    const d = drag
    drag = null
    commit(d)
  }

  function step(e: KeyboardEvent) {
    const i = DEPTHS.indexOf(browse.depth)
    const to = ({ ArrowRight: i + 1, ArrowUp: i + 1, ArrowLeft: i - 1, ArrowDown: i - 1, Home: 0, End: DEPTHS.length - 1 } as Record<string, number>)[e.key]
    if (to === undefined) return
    e.preventDefault()
    commit(DEPTHS[Math.min(DEPTHS.length - 1, Math.max(0, to))])
  }

  function key(e: KeyboardEvent) {
    const el = e.target as HTMLElement
    if (e.key === 'Backspace' && !el.closest('[role=dialog]') && !/^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName) && !el.isContentEditable) {
      e.preventDefault()
      browse.up()
    }
  }
</script>

<svelte:window onkeydown={key} />

{#snippet crumb(c: { label: string; rel: string }, last: boolean)}
  {#if last}
    <span class="here" aria-current="page" title={c.label}>{c.label}</span>
  {:else}
    <button class="crumb" title={c.label} onclick={() => go(c.rel)}>{c.label}</button>
  {/if}
{/snippet}

<div class="toolbar">
  <nav aria-label={t.browse.crumbs} class="crumbs">
    <button class="up" onclick={() => browse.up()} disabled={!browse.node?.parent_ref} aria-label={t.browse.up}>↑</button>
    <RootPicker label={trail[0]?.label ?? null} here={trail.length <= 1} ongo={() => go(trail[0].rel)} />
    {#each trail.slice(1) as c, i (c.rel)}
      <span aria-hidden="true">/</span>
      {@render crumb(c, i === trail.length - 2)}
    {/each}
  </nav>
  <FilterBar />
  <span class="grow"></span>
  <div class="depth" title={t.browse.depthHint}>
    <span class="label" aria-hidden="true">{t.browse.depth}</span>
    <div
      bind:this={meter}
      class="meter"
      class:dragging={drag !== null}
      role="slider"
      tabindex="0"
      aria-label={t.browse.depthHint}
      aria-valuemin={DEPTHS[0]}
      aria-valuemax={DEPTHS[DEPTHS.length - 1]}
      aria-valuenow={shown}
      aria-valuetext={t.browse.depthN(shown)}
      onpointerdown={down}
      onpointermove={move}
      onpointerup={release}
      onlostpointercapture={release}
      onpointercancel={() => (drag = null)}
      onkeydown={step}
    >
      {#each DEPTHS as d (d)}
        <span class="bar" class:in={d < shown} class:on={d === shown} style:height="{6 + d * 3}px"></span>
      {/each}
    </div>
    <span class="value" aria-hidden="true">{shown}</span>
  </div>
  {@render actions?.()}
</div>

<style>
  .toolbar {
    display: flex;
    gap: 12px;
    align-items: center;
    flex-wrap: wrap;
    padding: 8px 20px;
    border-bottom: var(--edge);
    background: var(--paper);
  }
  .crumbs {
    display: flex;
    gap: 4px;
    align-items: center;
    min-width: 0;
  }
  .crumb,
  .up {
    border: none;
    background: none;
    color: var(--terra);
    cursor: pointer;
    padding: 2px 4px;
    max-width: 240px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    unicode-bidi: isolate;
  }
  .here {
    font-weight: 500;
    max-width: 280px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    unicode-bidi: isolate;
  }
  .grow {
    flex: 1;
  }
  /* Detail meter: rising bars, filled up to the chosen level. The hit area is the
     whole meter, padded beyond the bars, so the small bars stay easy to grab. */
  .depth {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 13px;
    color: var(--ink-2);
  }
  .meter {
    display: flex;
    align-items: flex-end;
    gap: 3px;
    height: 21px;
    padding: 4px 6px;
    border-radius: 6px;
    cursor: pointer;
    touch-action: none;
    user-select: none;
  }
  .meter:focus-visible {
    outline: none;
    box-shadow: var(--focus);
  }
  .meter.dragging {
    cursor: grabbing;
  }
  .bar {
    width: 7px;
    border-radius: 2px;
    background: var(--hair);
    transition: background 80ms;
  }
  .bar.in {
    background: var(--mustard);
  }
  .bar.on {
    background: var(--ink);
  }
  .value {
    min-width: 1ch;
    font-family: var(--mono);
    color: var(--ink);
  }
</style>
