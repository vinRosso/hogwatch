<script lang="ts">
  import { onMount } from 'svelte'
  import { api } from '../../lib/api/client'
  import type { components } from '../../lib/api/schema'
  import { displayName } from '../../lib/fname'
  import { formatBytes } from '../../lib/format'
  import { t } from '../../lib/i18n/en'
  import { rootColor } from '../../lib/rootColor'
  import Card from '../../components/ui/Card.svelte'
  import Donut, { partsOf } from '../../components/ui/Donut.svelte'
  import EmptyState from '../../components/ui/EmptyState.svelte'

  type Mount = components['schemas']['MountStat']

  let mounts = $state<Mount[] | null>(null)
  let failed = $state(false)

  async function load() {
    failed = false
    try {
      const p = await api.get<{ items: Mount[] }>('/admin/mounts', { window: '30d' })
      mounts = p.items
    } catch {
      failed = true
    }
  }
  onMount(() => void load())

  // The bar is how full the filesystem under the root is.
  const parts = $derived(
    mounts === null
      ? []
      : partsOf(
          mounts.map((m) => ({
            name: displayName(m.root_name),
            color: rootColor(m.root_id),
            bytes: m.bytes_alloc ?? 0,
            gauge:
              m.fs_total !== null && m.fs_free !== null && m.fs_total > 0
                ? { fraction: (m.fs_total - m.fs_free) / m.fs_total, limit: formatBytes(m.fs_total) }
                : undefined,
          })),
          t.admin.overview.other,
        ),
  )
</script>

<Card title={t.admin.overview.roots}>
  {#if failed}
    <EmptyState title={t.common.error} body={t.admin.overview.failed} />
  {:else if mounts === null}
    <p>{t.common.loading}</p>
  {:else if parts.length === 0}
    <p>{t.admin.overview.noRoots}</p>
  {:else}
    <Donut {parts} caption={t.admin.overview.allocated} label={t.admin.overview.rootsChart} />
  {/if}
</Card>
