<script lang="ts">
  import { untrack, type Snippet } from 'svelte'
  import { t } from '../../lib/i18n/en'

  let { open, title, onclose, children }: { open: boolean; title: string; onclose: () => void; children?: Snippet } = $props()
  let box: HTMLDivElement | undefined = $state()

  // Focus moves into the dialog on open and returns to whatever opened it on close.
  $effect(() => {
    if (!open) return
    const opener = document.activeElement
    untrack(() => box?.focus())
    return () => {
      if (opener instanceof HTMLElement && opener.isConnected) opener.focus()
    }
  })

  const focusable = 'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'

  // Tab wraps inside the dialog instead of leaving it for the page behind.
  function trap(e: KeyboardEvent) {
    if (e.key !== 'Tab' || !box) return
    const els = [...box.querySelectorAll<HTMLElement>(focusable)]
    if (els.length === 0) return e.preventDefault()
    const first = els[0]
    const last = els[els.length - 1]
    const at = document.activeElement
    if (e.shiftKey && (at === first || at === box)) {
      e.preventDefault()
      last.focus()
    } else if (!e.shiftKey && at === last) {
      e.preventDefault()
      first.focus()
    }
  }
</script>

<svelte:window onkeydown={(e) => open && e.key === 'Escape' && onclose()} />

{#if open}
  <div class="backdrop" role="presentation" onclick={onclose}></div>
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <div class="dialog" role="dialog" aria-modal="true" aria-label={title} tabindex="-1" bind:this={box} onkeydown={trap}>
    <header>
      <h2>{title}</h2>
      <button class="x" onclick={onclose} aria-label={t.common.close}>×</button>
    </header>
    {@render children?.()}
  </div>
{/if}

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    background: rgba(31, 27, 22, 0.45);
    z-index: 50;
  }
  .dialog {
    position: fixed;
    z-index: 51;
    top: 12vh;
    left: 50%;
    transform: translateX(-50%);
    width: min(560px, 92vw);
    max-height: 76vh;
    overflow: auto;
    background: var(--card);
    border: var(--edge);
    border-radius: var(--r);
    box-shadow: 4px 4px 0 var(--ink);
    padding: 16px 18px;
  }
  header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 10px;
  }
  .x {
    border: none;
    background: none;
    font-size: 22px;
    cursor: pointer;
  }
</style>
