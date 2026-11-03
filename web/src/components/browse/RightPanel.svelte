<script lang="ts">
  import { browse } from '../../lib/browse.svelte'
  import { filterToParam } from '../../lib/filter'
  import { t } from '../../lib/i18n/en'
  import { router, setQuery } from '../../lib/router.svelte'
  import { session } from '../../lib/session.svelte'
  import Card from '../ui/Card.svelte'
  import Tabs from '../ui/Tabs.svelte'
  import ErrorsTab from './ErrorsTab.svelte'
  import ExtensionsTab from './ExtensionsTab.svelte'
  import HistoryTab from './HistoryTab.svelte'
  import LastScan from './LastScan.svelte'
  import ListTab from './ListTab.svelte'
  import TopTab from './TopTab.svelte'

  let { onlist }: { onlist?: (ref: string, mode?: 'children' | 'subtree') => void } = $props()

  // Scan errors are for admins; the history chart is its own card.
  const tabs = $derived([
    { id: 'list', label: t.tabs.list },
    { id: 'top', label: t.tabs.top },
    { id: 'ext', label: t.tabs.ext },
    ...(session.me?.is_admin ? [{ id: 'errors', label: t.tabs.errors }] : []),
  ])
  // The tab lives in the URL so Back and Forward work.
  const active = $derived(tabs.some((x) => x.id === router.query.tab) ? router.query.tab : 'list')
  const ref = $derived(browse.node?.ref ?? null)
</script>

<div class="panel">
  <div class="details">
    <Card>
      <Tabs {tabs} {active} label={t.tabs.label} onselect={(id) => setQuery({ tab: id === 'list' ? undefined : id })} />
      {#if ref}
        <div role="tabpanel" id={`panel-${active}`} aria-labelledby={`tab-${active}`} class="body">
          {#if active === 'list'}
            <ListTab {ref} filter={filterToParam(browse.filter)} />
          {:else if active === 'top'}
            <TopTab {ref} {onlist} />
          {:else if active === 'ext'}
            <ExtensionsTab {ref} files={browse.node?.files ?? 0} {onlist} />
          {:else}
            <ErrorsTab {ref} />
          {/if}
        </div>
      {/if}
    </Card>
  </div>
  {#if ref}
    <Card title={t.tabs.history}>
      {#snippet actions()}<LastScan />{/snippet}
      <HistoryTab {ref} />
    </Card>
  {/if}
</div>

<style>
  .panel {
    display: grid;
    flex: 1;
    grid-template-columns: minmax(0, 1fr);
    grid-template-rows: minmax(0, 1fr) auto;
    gap: 14px;
    min-width: 0;
    min-height: 0;
  }
  .details {
    display: flex;
    flex-direction: column;
    min-height: 0;
  }
  .details > :global(.card) {
    flex: 1;
    display: flex;
    flex-direction: column;
    min-height: 0;
  }
  .body {
    flex: 1;
    display: flex;
    flex-direction: column;
    min-height: 0;
    padding-top: 8px;
  }
</style>
