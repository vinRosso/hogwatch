<script lang="ts">
  import { onMount } from 'svelte'
  import { api } from '../../lib/api/client'
  import type { components } from '../../lib/api/schema'
  import { formatBytes } from '../../lib/format'
  import { t } from '../../lib/i18n/en'
  import { userColor } from '../../lib/userColor'
  import Card from '../../components/ui/Card.svelte'
  import Donut, { partsOf } from '../../components/ui/Donut.svelte'
  import EmptyState from '../../components/ui/EmptyState.svelte'

  type Row = components['schemas']['RankingRow']

  let rows = $state<Row[] | null>(null)
  let failed = $state(false)

  async function load() {
    failed = false
    try {
      const p = await api.get<{ items: Row[] }>('/admin/ranking', { sort: 'usage', window: '30d' })
      rows = p.items
    } catch {
      failed = true
    }
  }
  onMount(() => void load())

  // The bar is the server's quota percentage; a bar past full means over quota.
  const parts = $derived(
    rows === null
      ? []
      : partsOf(
          rows.map((r) => ({
            name: r.username,
            color: userColor(r.user_id),
            bytes: r.bytes_alloc,
            gauge:
              r.quota_bytes !== null && r.quota_pct !== null
                ? { fraction: r.quota_pct / 100, limit: formatBytes(r.quota_bytes) }
                : undefined,
          })),
          t.admin.overview.other,
        ),
  )
</script>

<Card title={t.admin.overview.users}>
  {#if failed}
    <EmptyState title={t.common.error} body={t.admin.overview.failed} />
  {:else if rows === null}
    <p>{t.common.loading}</p>
  {:else if parts.length === 0}
    <p>{t.admin.overview.noUsers}</p>
  {:else}
    <Donut {parts} caption={t.admin.overview.allocated} label={t.admin.overview.usersChart} />
  {/if}
</Card>
