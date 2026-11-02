<script lang="ts">
  import { api } from '../../lib/api/client'
  import type { RootInfo } from '../../lib/api/types'
  import { browse } from '../../lib/browse.svelte'
  import { formatAge } from '../../lib/format'
  import { t } from '../../lib/i18n/en'
  import { session } from '../../lib/session.svelte'
  import Pill from '../ui/Pill.svelte'

  const root = $derived(session.roots.find((r) => r.id === (browse.node?.root_id ?? session.rootId)) ?? null)
  const scanning = $derived(root?.scanning ?? false)

  // While a scan runs, watch for it to finish. A finished scan renumbers every
  // node, so the current ref is reloaded and the stale-ref recovery re-finds it.
  $effect(() => {
    if (!scanning) return
    const id = root!.id
    const before = root!.last_scanned_at
    const timer = setInterval(async () => {
      try {
        const res = await api.get<{ items: RootInfo[] }>('/roots')
        session.roots = res.items
        const now = res.items.find((r) => r.id === id)
        if (now && now.last_scanned_at !== before && browse.node) void browse.loadNode(browse.node.ref)
      } catch {
        /* try again on the next tick */
      }
    }, 10_000)
    return () => clearInterval(timer)
  })
</script>

{#if root}
  <p>
    {#if root.last_scanned_at === null}{t.scan.never}{:else}{t.scan.at} <span class="mono">{formatAge(root.last_scanned_at, Math.floor(Date.now() / 1000))}</span>{/if}
    {#if scanning}<Pill tone="warn">{t.scan.scanning}</Pill>{/if}
  </p>
{/if}

<style>
  p {
    margin: 0;
    display: flex;
    gap: 8px;
    align-items: center;
  }
</style>
