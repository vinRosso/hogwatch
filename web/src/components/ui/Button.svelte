<script lang="ts">
  import type { Snippet } from 'svelte'
  import type { HTMLButtonAttributes } from 'svelte/elements'

  let {
    variant = 'primary',
    children,
    ...rest
  }: { variant?: 'primary' | 'ghost' | 'danger'; children?: Snippet } & HTMLButtonAttributes = $props()
</script>

<button class={['btn', variant]} {...rest}>{@render children?.()}</button>

<style>
  .btn {
    border: var(--edge);
    border-radius: var(--r-pill);
    padding: 6px 14px;
    background: var(--card);
    cursor: pointer;
    font-weight: 500;
    box-shadow: var(--pop);
  }
  .btn:active:not(:disabled) {
    transform: translate(1px, 1px);
    box-shadow: 1px 1px 0 var(--ink);
  }
  .primary {
    background: var(--terra);
    color: var(--paper);
  }
  .danger {
    background: var(--danger);
    color: var(--paper);
  }
  .ghost {
    box-shadow: none;
    border-color: transparent;
    background: transparent;
  }
  .btn:disabled {
    opacity: 0.55;
    cursor: not-allowed;
  }
</style>
