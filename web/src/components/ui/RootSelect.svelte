<script lang="ts">
  import { displayName } from '../../lib/fname'
  import { t } from '../../lib/i18n/en'
  import { rootColor } from '../../lib/rootColor'

  // A root filter in the app's own style: a pill that opens a menu, each root with its color.
  let {
    label,
    roots,
    value,
    onchange,
  }: { label: string; roots: { id: number; name: string }[]; value: number | null; onchange: (id: number | null) => void } = $props()

  let open = $state(false)
  let wrap = $state<HTMLElement>()
  let pop = $state<HTMLElement>()

  const current = $derived(roots.find((r) => r.id === value))
  const shown = $derived(current ? displayName(current.name) : t.admin.ranking.allRoots)

  function items(): HTMLElement[] {
    return pop ? [...pop.querySelectorAll<HTMLElement>('[role=menuitemradio]')] : []
  }

  async function toggle() {
    open = !open
    if (!open) return
    await Promise.resolve()
    const all = items()
    ;(all.find((el) => el.getAttribute('aria-checked') === 'true') ?? all[0])?.focus()
  }

  function close(refocus = true) {
    open = false
    if (refocus) wrap?.querySelector<HTMLElement>('.trigger')?.focus()
  }

  function pick(id: number | null) {
    close()
    if (id !== value) onchange(id)
  }

  function nav(e: KeyboardEvent) {
    const all = items()
    const i = all.indexOf(document.activeElement as HTMLElement)
    const to = ({ ArrowDown: i + 1, ArrowUp: i - 1, Home: 0, End: all.length - 1 } as Record<string, number>)[e.key]
    if (e.key === 'Escape') {
      e.preventDefault()
      close()
    } else if (e.key === 'Tab') close(false)
    else if (to !== undefined) {
      e.preventDefault()
      all[(to + all.length) % all.length]?.focus()
    }
  }

  function outside(e: PointerEvent) {
    if (open && !wrap?.contains(e.target as Node)) close(false)
  }
</script>

<svelte:window onpointerdown={outside} />

<div class="sel" bind:this={wrap}>
  <span class="label">{label}</span>
  <button class="trigger" aria-haspopup="menu" aria-expanded={open} aria-label="{label}: {shown}" onclick={toggle}>
    <i class="dot" style:background={current ? rootColor(current.id) : 'var(--ink-3)'} aria-hidden="true"></i>
    <span class="name">{shown}</span>
    <svg class="chev" viewBox="0 0 10 6" aria-hidden="true"><path d="M1 1l4 4 4-4" /></svg>
  </button>
  {#if open}
    <div class="pop" role="menu" aria-label={label} tabindex="-1" bind:this={pop} onkeydown={nav}>
      <button role="menuitemradio" aria-checked={value === null} tabindex="-1" onclick={() => pick(null)}>
        <span class="tick" aria-hidden="true">{value === null ? '✓' : ''}</span>
        <i class="dot" style:background="var(--ink-3)" aria-hidden="true"></i>
        {t.admin.ranking.allRoots}
      </button>
      {#each roots as r (r.id)}
        <button role="menuitemradio" aria-checked={r.id === value} tabindex="-1" onclick={() => pick(r.id)}>
          <span class="tick" aria-hidden="true">{r.id === value ? '✓' : ''}</span>
          <i class="dot" style:background={rootColor(r.id)} aria-hidden="true"></i>
          <span style="unicode-bidi: isolate">{displayName(r.name)}</span>
        </button>
      {/each}
    </div>
  {/if}
</div>

<style>
  .sel {
    position: relative;
    display: flex;
    gap: 6px;
    align-items: center;
    min-width: 0;
  }
  .label {
    color: var(--ink);
  }
  .trigger {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    max-width: 240px;
    border: var(--edge);
    border-radius: var(--r-pill);
    background: var(--card);
    padding: 4px 10px 4px 12px;
    font: inherit;
    color: var(--ink);
    cursor: pointer;
  }
  .trigger:hover {
    background: var(--sunk);
  }
  .trigger:focus-visible {
    outline: none;
    box-shadow: var(--focus);
  }
  .name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    unicode-bidi: isolate;
  }
  .dot {
    flex: none;
    width: 10px;
    height: 10px;
    border-radius: 50%;
  }
  .chev {
    flex: none;
    width: 10px;
    height: 6px;
    fill: none;
    stroke: var(--ink-2);
    stroke-width: 1.5;
    stroke-linecap: round;
    stroke-linejoin: round;
  }
  .pop {
    position: absolute;
    left: 0;
    top: calc(100% + 6px);
    min-width: 100%;
    max-width: 320px;
    background: var(--card);
    border: var(--edge);
    border-radius: var(--r);
    box-shadow: var(--pop);
    overflow: hidden;
    display: grid;
    padding: 4px 0;
    z-index: 40;
  }
  .pop button {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 12px;
    text-align: left;
    white-space: nowrap;
    background: none;
    border: none;
    font: inherit;
    color: var(--ink);
    cursor: pointer;
  }
  .pop button:hover,
  .pop button:focus-visible {
    outline: none;
    background: var(--sunk);
  }
  .tick {
    width: 1ch;
    color: var(--terra);
  }
</style>
