<script lang="ts">
  import { untrack, type Snippet } from 'svelte'
  import { visibleRange } from '../../lib/virtual'

  let {
    count,
    rowHeight = 32,
    height = 420,
    fill = false,
    row,
    load,
    label,
  }: {
    count: number
    rowHeight?: number
    height?: number
    fill?: boolean
    row: Snippet<[number]>
    load?: (start: number, end: number) => void
    label?: string
  } = $props()

  let top = $state(0)
  let viewport = $state(0)
  const range = $derived(visibleRange(top, viewport || height, rowHeight, count))

  $effect(() => {
    const { start, end } = range
    untrack(() => load?.(start, end))
  })
</script>

<div
  class="vl"
  class:fill
  role="list"
  aria-label={label}
  style:height={fill ? undefined : `${Math.min(height, count * rowHeight)}px`}
  bind:clientHeight={viewport}
  onscroll={(e) => (top = e.currentTarget.scrollTop)}
>
  <div class="inner" style:height={`${count * rowHeight}px`}>
    {#each { length: range.end - range.start } as _, k (range.start + k)}
      <div class="r" role="listitem" style:top={`${(range.start + k) * rowHeight}px`} style:height={`${rowHeight}px`}>
        {@render row(range.start + k)}
      </div>
    {/each}
  </div>
</div>

<style>
  .vl {
    overflow-y: auto;
    /* A fixed gutter, so a header above the list can reserve the same width. */
    scrollbar-gutter: stable;
    position: relative;
  }
  .vl.fill {
    flex: 1 1 auto;
    min-height: 0;
  }
  .inner {
    position: relative;
  }
  .r {
    position: absolute;
    left: 0;
    right: 0;
  }
</style>
