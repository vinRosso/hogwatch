<script lang="ts">
  import { onMount } from 'svelte'
  import { api } from '../../lib/api/client'
  import type { components } from '../../lib/api/schema'
  import { displayName } from '../../lib/fname'
  import { formatDate } from '../../lib/format'
  import { t } from '../../lib/i18n/en'
  import Button from '../../components/ui/Button.svelte'
  import EmptyState from '../../components/ui/EmptyState.svelte'
  import UserName from '../../components/ui/UserName.svelte'

  type Entry = components['schemas']['AuditPage']['items'][number]
  let items = $state<Entry[] | null>(null)
  let cursor = $state<string | null>(null)
  let failed = $state(false)

  let paging = false
  async function load(more: boolean) {
    if (more) {
      if (paging) return
      paging = true
    }
    failed = false
    try {
      const p = await api.get<{ items: Entry[]; next_cursor: string | null }>('/admin/audit', { cursor: more ? (cursor ?? undefined) : undefined })
      items = more ? [...(items ?? []), ...p.items] : p.items
      cursor = p.next_cursor
    } catch {
      failed = true
    } finally {
      paging = false
    }
  }
  // The log stores user ids; the names come from the user list.
  let names = $state(new Map<number, string>())
  async function loadNames() {
    try {
      const p = await api.get<{ items: { id: number; username: string; display_name: string }[] }>('/admin/users')
      names = new Map(p.items.map((u) => [u.id, u.display_name || u.username]))
    } catch {
      names = new Map()
    }
  }

  onMount(() => {
    void load(false)
    void loadNames()
  })
</script>

<section>
  <h1>{t.admin.sections.audit}</h1>
  {#if failed}
    <EmptyState title={t.common.error} body={t.admin.audit.failed}>
      <Button onclick={() => load(false)}>{t.common.retry}</Button>
    </EmptyState>
  {:else if items === null}
    <p>{t.common.loading}</p>
  {:else if items.length === 0}
    <EmptyState title={t.admin.sections.audit} body={t.admin.audit.empty} />
  {:else}
    <table>
      <thead>
        <tr><th>{t.admin.audit.ts}</th><th>{t.admin.audit.actor}</th><th>{t.admin.audit.action}</th><th>{t.admin.audit.target}</th><th>{t.admin.audit.details}</th></tr>
      </thead>
      <tbody>
        {#each items as e (e.id)}
          <tr>
            <td class="mono">{formatDate(e.ts)}</td>
            <td class="mono">{#if e.actor_id === null}{t.common.none}{:else}<UserName id={e.actor_id} name={names.get(e.actor_id) ?? String(e.actor_id)} />{/if}</td>
            <td>{e.action}</td>
            <td class="mono" style="unicode-bidi: isolate">{displayName(e.target)}</td>
            <td><pre style="unicode-bidi: isolate">{displayName(JSON.stringify(e.details))}</pre></td>
          </tr>
        {/each}
      </tbody>
    </table>
    {#if cursor}<Button variant="ghost" onclick={() => load(true)}>{t.admin.audit.more}</Button>{/if}
  {/if}
</section>

<style>
  h1 {
    font: 600 24px var(--serif);
    margin: 0 0 12px;
  }
  table {
    width: 100%;
    border-collapse: collapse;
  }
  th,
  td {
    text-align: left;
    padding: 5px 8px;
    border-bottom: 1px solid var(--hair);
    vertical-align: top;
  }
  th {
    color: var(--ink-2);
    font-weight: 500;
    font-size: 12px;
  }
  pre {
    margin: 0;
    font: 12px var(--mono);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    max-width: 420px;
  }
</style>
