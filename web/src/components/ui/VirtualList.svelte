<script lang="ts">
  import { untrack, type Snippet } from 'svelte'
  import { visibleRange } from '../../lib/virtual'

  let {
    count,
    rowHeight = 32,
    height = 420,
    row,
    load,
    label,
  }: {
    count: number
    rowHeight?: number
    height?: number
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
  role="list"
  aria-label={label}
  style:height={`${Math.min(height, count * rowHeight)}px`}
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
    position: relative;
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
