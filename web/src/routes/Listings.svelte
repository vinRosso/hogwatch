<script lang="ts">
  import EmptyState from '../components/ui/EmptyState.svelte'
  import { t } from '../lib/i18n/en'
  import { recentListings } from '../lib/listings'

  const ids = recentListings()
</script>

<section class="page">
  <h1>{t.listing.listTitle}</h1>
  {#if ids.length === 0}
    <EmptyState title={t.listing.listTitle} body={t.listing.listEmpty} />
  {:else}
    <ul>
      {#each ids as id (id)}
        <li><a href={`/listings/${id}`}>{t.listing.open(id)}</a></li>
      {/each}
    </ul>
    <p class="note">{t.listing.listEmpty}</p>
  {/if}
</section>

<style>
  .page {
    padding: 16px 20px;
  }
  h1 {
    font: 600 24px var(--serif);
    margin: 0 0 12px;
  }
  .note {
    color: var(--ink-2);
  }
</style>
