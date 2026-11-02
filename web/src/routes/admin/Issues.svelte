<script lang="ts">
  import { onMount } from 'svelte'
  import { api } from '../../lib/api/client'
  import { displayName } from '../../lib/fname'
  import type { components } from '../../lib/api/schema'
  import { t } from '../../lib/i18n/en'
  import Button from '../../components/ui/Button.svelte'
  import EmptyState from '../../components/ui/EmptyState.svelte'
  import RootName from '../../components/ui/RootName.svelte'

  type Issue = components['schemas']['ConfigIssue']
  const REF = /^[1-9][0-9]*\.[1-9][0-9]*$/

  let items = $state<Issue[] | null>(null)
  let failed = $state(false)
  let roots = $state<Record<number, string>>({})

  async function load() {
    failed = false
    try {
      items = (await api.get<{ items: Issue[] }>('/admin/config-issues')).items
    } catch {
      failed = true
    }
  }
  onMount(() => {
    void load()
    api.get<{ items: { id: number; name: string }[] }>('/admin/roots').then(
      (p) => (roots = Object.fromEntries(p.items.map((r) => [r.id, r.name]))),
      () => {},
    )
  })

  const groups = $derived.by(() => {
    const m = new Map<string, Issue[]>()
    for (const i of items ?? []) m.set(i.kind, [...(m.get(i.kind) ?? []), i])
    return [...m.entries()]
  })

  type Part = { text: string; ref?: string }
  /** A detail value as text, with node references (<scan>.<dir>) split out as links. */
  function parts(key: string, v: unknown): Part[] {
    if ((key === 'node' || key === 'nodes') && typeof v === 'string' && REF.test(v)) return [{ text: v, ref: v }]
    if (key === 'nodes' && Array.isArray(v)) return v.map((x) => (typeof x === 'string' && REF.test(x) ? { text: x, ref: x } : { text: String(x) }))
    return [{ text: displayName(typeof v === 'string' ? v : JSON.stringify(v)) }]
  }
</script>

<section>
  <h1>{t.admin.sections.issues}</h1>
  {#if failed}
    <EmptyState title={t.common.error} body={t.admin.issues.failed}>
      <Button onclick={load}>{t.common.retry}</Button>
    </EmptyState>
  {:else if items === null}
    <p>{t.common.loading}</p>
  {:else if items.length === 0}
    <EmptyState title={t.admin.sections.issues} body={t.admin.issues.none} />
  {:else}
    {#each groups as [kind, list] (kind)}
      <h2>{t.admin.issues.kinds[kind] ?? kind} <span class="n">{list.length}</span></h2>
      <ul>
        {#each list as i, k (i.id ?? `d${k}`)}
          <li>
            <span class="root">{t.admin.issues.root}: <RootName id={i.root_id} name={roots[i.root_id] ?? String(i.root_id)} /></span>
            {#if i.id === null}<em>{t.admin.issues.computed}</em>{/if}
            <dl>
              {#each Object.entries(i.details ?? {}) as [key, v] (key)}
                <dt>{key}</dt>
                <dd class="mono" style="unicode-bidi: isolate">
                  {#each parts(key, v) as p, pi (pi)}
                    {#if p.ref}<a href={`/browse/${p.ref}`}>{p.text}</a>{:else}{p.text}{/if}{' '}
                  {/each}
                </dd>
              {/each}
            </dl>
          </li>
        {/each}
      </ul>
    {/each}
  {/if}
</section>

<style>
  h1 {
    font: 600 24px var(--serif);
    margin: 0 0 12px;
  }
  h2 {
    font: 600 17px var(--serif);
    margin: 16px 0 6px;
  }
  .n {
    color: var(--ink-2);
    font-weight: 400;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  li {
    padding: 8px 0;
    border-bottom: 1px solid var(--hair);
  }
  .root {
    font-weight: 500;
    margin-right: 8px;
  }
  dl {
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 2px 12px;
    margin: 4px 0 0;
  }
  dt {
    color: var(--ink-2);
  }
  dd {
    margin: 0;
    overflow-wrap: anywhere;
  }
</style>
