<script lang="ts">
  import { dismissToast, toasts } from '../../lib/toast.svelte'
  import { t } from '../../lib/i18n/en'

  // Saved confirmations sit at the top centre, where they are seen; other messages stay in the corner.
  const saved = $derived(toasts.items.filter((x) => x.kind === 'saved'))
  const corner = $derived(toasts.items.filter((x) => x.kind !== 'saved'))
</script>

{#if saved.length > 0}
  <div class="top" role="status" aria-live="polite">
    {#each saved as it (it.id)}
      <div class={['toast', it.kind]}>
        <span>{it.text}</span>
        <button onclick={() => dismissToast(it.id)} aria-label={t.common.dismiss}>×</button>
      </div>
    {/each}
  </div>
{/if}

<div class="stack" role="status" aria-live="polite">
  {#each corner as it (it.id)}
    <div class={['toast', it.kind]}>
      <span>{it.text}</span>
      <button onclick={() => dismissToast(it.id)} aria-label={t.common.dismiss}>×</button>
    </div>
  {/each}
</div>

<style>
  .stack,
  .top {
    position: fixed;
    display: grid;
    gap: 8px;
    z-index: 60;
  }
  .stack {
    right: 16px;
    bottom: 16px;
  }
  .top {
    top: 16px;
    left: 50%;
    transform: translateX(-50%);
  }
  .top .saved {
    background: var(--sage);
    border-color: var(--ink);
    color: var(--paper);
    font-size: 16px;
    font-weight: 500;
    padding: 10px 16px;
  }
  .toast {
    display: flex;
    gap: 10px;
    align-items: center;
    background: var(--card);
    border: var(--edge);
    border-radius: var(--r);
    box-shadow: var(--pop);
    padding: 8px 12px;
  }
  .error {
    background: var(--danger-bg);
  }
  button {
    border: none;
    background: none;
    cursor: pointer;
    font-size: 18px;
  }
</style>
