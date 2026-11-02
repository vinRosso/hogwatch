<script lang="ts">
  import { tick } from 'svelte'

  type Tab = { id: string; label: string }
  let { tabs, active, onselect, label }: { tabs: Tab[]; active: string; onselect: (id: string) => void; label?: string } = $props()

  function key(e: KeyboardEvent, i: number) {
    let to = -1
    if (e.key === 'ArrowRight') to = (i + 1) % tabs.length
    else if (e.key === 'ArrowLeft') to = (i - 1 + tabs.length) % tabs.length
    else if (e.key === 'Home') to = 0
    else if (e.key === 'End') to = tabs.length - 1
    if (to < 0) return
    e.preventDefault()
    onselect(tabs[to].id)
    void tick().then(() => document.getElementById(`tab-${tabs[to].id}`)?.focus())
  }
</script>

<div role="tablist" aria-label={label} class="tabs">
  {#each tabs as tab, i (tab.id)}
    <button
      role="tab"
      id={`tab-${tab.id}`}
      aria-selected={tab.id === active}
      aria-controls={`panel-${tab.id}`}
      tabindex={tab.id === active ? 0 : -1}
      class:on={tab.id === active}
      onclick={() => onselect(tab.id)}
      onkeydown={(e) => key(e, i)}>{tab.label}</button
    >
  {/each}
</div>

<style>
  .tabs {
    display: flex;
    gap: 4px;
    border-bottom: var(--edge);
    flex-wrap: wrap;
  }
  button {
    border: var(--edge);
    border-bottom: none;
    border-radius: var(--r) var(--r) 0 0;
    background: var(--sunk);
    padding: 5px 12px;
    cursor: pointer;
    margin-bottom: -1px;
  }
  button.on {
    background: var(--card);
    font-weight: 500;
  }
</style>
