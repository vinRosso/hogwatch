<script lang="ts">
  import type { Snippet } from 'svelte'

  // `head` replaces a plain `title` when the heading needs markup, such as a colored name.
  let { title, head, actions, children }: { title?: string; head?: Snippet; actions?: Snippet; children?: Snippet } = $props()
</script>

<section class="card">
  {#if actions}
    <div class="head">
      {#if head || title}<h2>{#if head}{@render head()}{:else}{title}{/if}</h2>{/if}
      <div class="actions">{@render actions()}</div>
    </div>
  {:else if head || title}<h2>{#if head}{@render head()}{:else}{title}{/if}</h2>{/if}
  {@render children?.()}
</section>

<style>
  .card {
    background: var(--card);
    border: var(--edge);
    border-radius: var(--r);
    box-shadow: var(--pop);
    padding: 14px 16px;
  }
  h2 {
    font-size: 16px;
    margin-bottom: 8px;
  }
  /* Fixed height, the size of a button: the card does not change size when the actions appear or go. */
  .head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    min-height: 34px;
    margin-bottom: 8px;
  }
  .head h2 {
    margin: 0;
  }
  .actions {
    display: flex;
    gap: 8px;
    margin-left: auto;
  }
</style>
