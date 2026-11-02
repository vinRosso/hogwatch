<script lang="ts">
  import type { Snippet } from 'svelte'

  let { title, body, children }: { title: string; body?: string | null; children?: Snippet } = $props()
</script>

<div class="empty">
  <h2>{title}</h2>
  {#if body}<p>{body}</p>{/if}
  {@render children?.()}
</div>

<style>
  .empty {
    text-align: center;
    padding: 32px 16px;
    color: var(--ink-2);
  }
  h2 {
    color: var(--ink);
    margin-bottom: 6px;
  }
  p {
    margin: 0 0 10px;
  }
</style>
