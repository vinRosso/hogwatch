<script lang="ts">
  import type { Snippet } from 'svelte'

  let { tone = 'neutral', children }: { tone?: 'neutral' | 'warn' | 'danger' | 'ok'; children?: Snippet } = $props()
</script>

<span class={['pill', tone]}>{@render children?.()}</span>

<style>
  .pill {
    display: inline-block;
    border: var(--edge);
    border-radius: var(--r-pill);
    padding: 1px 9px;
    font-size: 12px;
    font-weight: 500;
    background: var(--card);
  }
  .warn {
    background: var(--mustard-bg);
  }
  .danger {
    background: var(--danger-bg);
  }
  .ok {
    background: var(--sage-bg);
  }
</style>
