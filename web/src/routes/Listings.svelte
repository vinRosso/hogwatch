<script lang="ts">
  import { onMount } from 'svelte'
  import { api, ApiError } from '../lib/api/client'
  import type { Listing } from '../lib/api/types'
  import { formatDateTime } from '../lib/format'
  import { t } from '../lib/i18n/en'
  import { clearListings, forgetListing, listingSummary, listingTitle, recentListings } from '../lib/listings'
  import Button from '../components/ui/Button.svelte'
  import Card from '../components/ui/Card.svelte'
  import EmptyState from '../components/ui/EmptyState.svelte'
  import Pill from '../components/ui/Pill.svelte'

  type State = { status: string; rows: number; more: boolean } | 'gone' | null

  let items = $state(recentListings())
  let live = $state<Record<number, State>>({})

  // One request per listing for its status and how many files the first page holds.
  onMount(() => {
    for (const m of items) {
      api
        .get<Listing>(`/listings/${m.id}`)
        .then((p) => (live[m.id] = { status: p.status, rows: p.items.length, more: p.next_cursor !== null }))
        .catch((e) => (live[m.id] = e instanceof ApiError && e.status === 404 ? 'gone' : null))
    }
  })

  function forget(id: number) {
    forgetListing(id)
    items = recentListings()
  }

  function clear() {
    clearListings()
    items = []
  }

  const tone = (s: string) => (s === 'done' ? 'ok' : s === 'failed' ? 'danger' : 'neutral')
</script>

<section class="page">
  <div class="head">
    <h1>{t.listing.listTitle}</h1>
    {#if items.length > 0}<Button variant="ghost" onclick={clear}>{t.listing.clear}</Button>{/if}
  </div>
  {#if items.length === 0}
    <EmptyState title={t.listing.listTitle} body={t.listing.listEmpty} />
  {:else}
    <ul>
      {#each items as m (m.id)}
        {@const st = live[m.id]}
        <li>
          <Card>
            <div class="row" class:gone={st === 'gone'}>
              <div class="main">
                <a class="title" href={`/listings/${m.id}`}>{listingTitle(m)}</a>
                {#if listingSummary(m)}<span class="sum">{listingSummary(m)}</span>{/if}
                {#if m.at}<span class="sum">{t.listing.started} {formatDateTime(m.at)} UTC</span>{/if}
              </div>
              <div class="side">
                {#if st === 'gone'}
                  <Pill>{t.listing.gone}</Pill>
                {:else if st}
                  <Pill tone={tone(st.status)}>{t.listing.status[st.status] ?? st.status}</Pill>
                  <span class="mono">{t.listing.rows(st.rows, st.more)}</span>
                {/if}
                <Button variant="ghost" onclick={() => forget(m.id)} aria-label={`${t.listing.forget} ${listingTitle(m)}`}>{t.listing.forget}</Button>
              </div>
            </div>
          </Card>
        </li>
      {/each}
    </ul>
    <p class="note">{t.listing.listHint}</p>
  {/if}
</section>

<style>
  .page {
    padding: 16px 20px;
    max-width: 880px;
    margin: 0 auto;
  }
  .head {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 12px;
    margin-bottom: 12px;
  }
  h1 {
    font: 600 24px var(--serif);
    margin: 0;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: 12px;
  }
  .row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
  }
  .row.gone {
    opacity: 0.6;
  }
  .main {
    display: grid;
    gap: 2px;
    min-width: 0;
  }
  .title {
    font-weight: 600;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    unicode-bidi: isolate;
  }
  .sum {
    color: var(--ink-2);
    font-size: 13px;
  }
  .side {
    display: flex;
    align-items: center;
    gap: 10px;
    flex: none;
  }
  .mono {
    font-family: var(--mono);
    font-size: 13px;
    color: var(--ink-2);
  }
  .note {
    color: var(--ink-2);
    margin-top: 12px;
  }
</style>
