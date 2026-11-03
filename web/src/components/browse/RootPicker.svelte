<script lang="ts">
  import { displayName } from '../../lib/fname'
  import { formatBytes } from '../../lib/format'
  import { rootColor } from '../../lib/rootColor'
  import { t } from '../../lib/i18n/en'
  import { session } from '../../lib/session.svelte'

  // The first breadcrumb and the root picker are one control: its menu lists the roots;
  // picking another root switches to it, picking the current one goes back to its top.
  let { label, here = true, ongo }: { label: string | null; here?: boolean; ongo: () => void } = $props()

  let open = $state(false)
  let wrap: HTMLElement
  let pop = $state<HTMLElement>()

  function rootLabel(id: number, name: string): string {
    const m = session.usage?.mounts.find((x) => x.root_id === id)
    if (!m) return displayName(name)
    const g = m.growth_bytes === null ? '' : `, ${m.growth_bytes < 0 ? '−' : '+'}${formatBytes(Math.abs(m.growth_bytes))} (30 d)`
    return `${displayName(name)} · ${formatBytes(m.usage.bytes_alloc)}${g}`
  }

  const current = $derived(session.roots.find((r) => r.id === session.rootId))
  const dot = $derived(current ? rootColor(current.id) : 'var(--ink-3)')
  const shown = $derived(label ?? (current ? displayName(current.name) : t.rootPicker.none))
  // A user with one root has nothing to switch to, so the menu is never offered.
  const single = $derived(session.roots.length <= 1)

  function items(): HTMLElement[] {
    return pop ? [...pop.querySelectorAll<HTMLElement>('[role=menuitemradio]')] : []
  }

  async function toggle() {
    if (single) return
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

  function pick(id: number) {
    close()
    if (id !== session.rootId) {
      session.rootId = id
      session.setPrefs({ root_id: id })
    } else if (!here) ongo()
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
    if (open && !wrap.contains(e.target as Node)) close(false)
  }
</script>

<svelte:window onpointerdown={outside} />

<div class="picker" bind:this={wrap}>
  {#if here && single}
    <!-- One root: nothing to pick from, so the name is plain text with no arrow. -->
    <span class="trigger here solo" title={shown} aria-label={shown} aria-current={label !== null ? 'page' : undefined}>
      <i class="dot" style:background={dot} aria-hidden="true"></i>
      <span class="name">{shown}</span>
    </span>
  {:else if here}
    <button
      class="trigger here"
      title={shown}
      aria-haspopup="menu"
      aria-expanded={open}
      aria-label="{t.rootPicker.label}: {shown}"
      aria-current={label !== null ? 'page' : undefined}
      disabled={session.roots.length === 0}
      onclick={toggle}
    >
      <i class="dot" style:background={dot} aria-hidden="true"></i>
      <span class="name">{shown}</span>
      <svg class="chev" viewBox="0 0 10 6" aria-hidden="true"><path d="M1 1l4 4 4-4" /></svg>
    </button>
  {:else}
    <span class="split">
      <button class="part name-btn" class:solo={single} title={shown} onclick={ongo}>
        <i class="dot" style:background={dot} aria-hidden="true"></i>
        <span class="name">{shown}</span>
      </button>
      {#if !single}
        <button
          class="part trigger"
          aria-haspopup="menu"
          aria-expanded={open}
          aria-label={t.rootPicker.label}
          disabled={session.roots.length === 0}
          onclick={toggle}
        >
          <svg class="chev" viewBox="0 0 10 6" aria-hidden="true"><path d="M1 1l4 4 4-4" /></svg>
        </button>
      {/if}
    </span>
  {/if}
  {#if open}
    <div class="pop" role="menu" aria-label={t.rootPicker.label} tabindex="-1" bind:this={pop} onkeydown={nav}>
      {#each session.roots as r (r.id)}
        <button role="menuitemradio" aria-checked={r.id === session.rootId} tabindex="-1" onclick={() => pick(r.id)}>
          <span class="tick" aria-hidden="true">{r.id === session.rootId ? '✓' : ''}</span>
          <i class="dot" style:background={rootColor(r.id)} aria-hidden="true"></i>
          {rootLabel(r.id, r.name)}
        </button>
      {/each}
    </div>
  {/if}
</div>

<style>
  .picker {
    position: relative;
    min-width: 0;
  }
  .trigger {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    max-width: 320px;
    border: none;
    border-radius: var(--r-pill);
    background: none;
    padding: 4px 10px 4px 12px;
    color: var(--terra);
    cursor: pointer;
  }
  .trigger:hover:not(:disabled),
  .name-btn:hover {
    background: var(--sunk);
  }
  .trigger.here {
    color: var(--ink);
    font-weight: 500;
  }
  .split {
    display: inline-flex;
    align-items: stretch;
    max-width: 320px;
  }
  .part {
    display: inline-flex;
    align-items: center;
    border: none;
    background: none;
    cursor: pointer;
    min-width: 0;
  }
  .name-btn {
    padding: 4px 0 4px 12px;
    color: var(--terra);
    border-radius: var(--r-pill) 0 0 var(--r-pill);
  }
  .split .trigger {
    padding: 4px 10px 4px 6px;
    border-radius: 0 var(--r-pill) var(--r-pill) 0;
  }
  .name-btn:focus-visible {
    outline: none;
    box-shadow: var(--focus);
  }
  .trigger:disabled {
    cursor: default;
    color: var(--ink-2);
  }
  .trigger.solo {
    cursor: default;
  }
  .trigger.solo:hover {
    background: none;
  }
  .name-btn.solo {
    cursor: default;
  }
  .name-btn.solo {
    padding-right: 10px;
    border-radius: var(--r-pill);
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
    max-width: 420px;
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
    color: var(--ink);
    cursor: pointer;
    unicode-bidi: isolate;
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
