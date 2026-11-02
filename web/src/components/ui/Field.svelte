<script lang="ts">
  import type { Snippet } from 'svelte'

  let { label, error, hint, children }: { label: string; error?: string | null; hint?: string; children: Snippet } = $props()
</script>

<label class="field">
  <span class="lab">{label}</span>
  {@render children()}
  {#if hint}<span class="hint">{hint}</span>{/if}
  {#if error}<span class="err" role="alert">{error}</span>{/if}
</label>

<style>
  .field {
    display: grid;
    gap: 4px;
    margin-bottom: 12px;
  }
  .lab {
    font-weight: 500;
  }
  .hint {
    color: var(--ink-2);
    font-size: 12px;
  }
  .err {
    color: var(--danger);
    font-size: 13px;
  }
  .field :global(input),
  .field :global(select),
  .field :global(textarea) {
    border: var(--edge);
    border-radius: 6px;
    padding: 7px 9px;
    background: var(--paper);
  }
</style>
