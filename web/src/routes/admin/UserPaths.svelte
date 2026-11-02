<script lang="ts">
  import { onMount } from 'svelte'
  import { api } from '../../lib/api/client'
  import type { AdminRoot, UserPath } from '../../lib/api/types'
  import { displayName } from '../../lib/fname'
  import { problemText } from '../../lib/errors'
  import { t } from '../../lib/i18n/en'
  import { checkRelPath } from '../../lib/patch'
  import Button from '../../components/ui/Button.svelte'
  import Card from '../../components/ui/Card.svelte'
  import Confirm from '../../components/ui/Confirm.svelte'
  import Pill from '../../components/ui/Pill.svelte'
  import RootName from '../../components/ui/RootName.svelte'
  import DirPicker from '../../components/admin/DirPicker.svelte'

  let { userId }: { userId: number } = $props()

  let paths = $state<UserPath[]>([])
  let roots = $state<AdminRoot[]>([])
  let adding = $state<number | null>(null)
  let rel = $state('')
  let error = $state<string | null>(null)
  let loadFailed = $state(false)
  let loaded = $state(false)
  let picking = $state(false)
  let removing = $state<UserPath | null>(null)
  let removeError = $state<string | null>(null)

  const rootName = (id: number) => roots.find((r) => r.id === id)?.name ?? String(id)
  const relError = $derived.by(() => {
    const e = checkRelPath(rel)
    return e ? t.admin.paths.err[e] : null
  })
  // Every root gets a group, so a root with nothing assigned still offers its Add; a path whose root is unknown still shows.
  const groups = $derived(
    [...new Set([...roots.map((r) => r.id), ...paths.map((p) => p.root_id)])].map((id) => ({
      id,
      items: paths.filter((p) => p.root_id === id),
    })),
  )

  async function load() {
    loadFailed = false
    try {
      const [p, r] = await Promise.all([
        api.get<{ items: UserPath[] }>(`/admin/users/${userId}/paths`),
        api.get<{ items: AdminRoot[] }>('/admin/roots'),
      ])
      paths = p.items
      roots = r.items
      loaded = true
    } catch {
      loadFailed = true
    }
  }
  onMount(() => void load())

  function open(id: number) {
    adding = id
    rel = ''
    error = null
  }

  async function assign(relPath: string) {
    if (adding === null) return
    error = null
    try {
      await api.post(`/admin/users/${userId}/paths`, { root_id: adding, rel_path: relPath })
      adding = null
      rel = ''
      await load()
    } catch (err) {
      // 409 (same path) and 422 (overlap, names the other user) carry the reason in detail.
      error = problemText(err, t.admin.failed)
    }
  }

  function submit(e: Event) {
    e.preventDefault()
    if (rel !== '' && !relError) void assign(rel)
  }

  async function remove() {
    if (!removing) return
    removeError = null
    try {
      await api.del(`/admin/users/${userId}/paths/${removing.id}`)
      removing = null
      await load()
    } catch (err) {
      removeError = problemText(err, t.admin.failed)
    }
  }

  const focus = (el: HTMLElement) => el.focus()
</script>

<Card title={t.admin.paths.title}>
  {#if loadFailed}
    <p class="err" role="alert">{t.admin.failed}</p>
  {:else if !loaded}
    <p class="muted">{t.common.loading}</p>
  {:else if roots.length === 0 && paths.length === 0}
    <p class="muted">{t.admin.paths.noRoots}</p>
  {/if}

  {#each groups as g (g.id)}
    {@const whole = g.items.some((p) => p.rel_path === '')}
    <section class="group">
      <header>
        <h3 class="mono"><RootName id={g.id} name={rootName(g.id)} /></h3>
        {#if adding !== g.id}
          <Button variant="ghost" onclick={() => open(g.id)} aria-label={t.admin.paths.addTo(displayName(rootName(g.id)))}>+ {t.admin.paths.addFolder}</Button>
        {/if}
      </header>

      {#if g.items.length === 0 && adding !== g.id}
        <p class="none">{t.admin.paths.nothing}</p>
      {:else if g.items.length > 0}
        <ul>
          {#each g.items as p (p.id)}
            <li>
              <div class="what">
                {#if p.rel_path === ''}
                  <span class="whole">{t.admin.paths.wholeRoot}</span>
                {:else}
                  <span class="p mono" style="unicode-bidi: isolate">/{displayName(p.rel_path)}</span>
                {/if}
                {#if !p.found}<Pill tone="warn">{t.admin.paths.notFound}</Pill>{/if}
                {#if p.redundant}<Pill>{t.admin.paths.redundant}</Pill>{/if}
              </div>
              <Button variant="ghost" onclick={() => ((removing = p), (removeError = null))}>{t.admin.paths.remove}</Button>
            </li>
          {/each}
        </ul>
      {/if}

      {#if adding === g.id}
        <form onsubmit={submit} novalidate>
          <div class="input" class:bad={!!relError}>
            <span class="mono" aria-hidden="true">/</span>
            <input
              class="mono"
              aria-label={t.admin.paths.path}
              bind:value={rel}
              use:focus
              autocomplete="off"
              spellcheck="false"
              onkeydown={(e) => {
                if (e.key === 'Escape') adding = null
              }}
            />
            <button type="button" class="browse" onclick={() => (picking = true)}>{t.admin.paths.browse}</button>
          </div>
          {#if relError}
            <p class="err" role="alert">{relError}</p>
          {:else}
            <p class="hint">{t.admin.paths.pathHint}</p>
          {/if}
          {#if error}<p class="err" role="alert">{error}</p>{/if}
          <div class="row">
            <Button type="submit" disabled={rel === '' || !!relError}>{t.admin.paths.add}</Button>
            {#if !whole}
              <Button type="button" variant="ghost" onclick={() => assign('')}>{t.admin.paths.assignRoot}</Button>
            {/if}
            <span class="grow"></span>
            <Button type="button" variant="ghost" onclick={() => (adding = null)}>{t.common.cancel}</Button>
          </div>
        </form>
      {/if}
    </section>
  {/each}
</Card>

{#if adding !== null}
  <DirPicker open={picking} rootId={adding} onpick={(p) => (rel = p)} onclose={() => (picking = false)} />
{/if}
<Confirm
  open={removing !== null}
  title={t.admin.paths.remove}
  message={removing ? t.admin.paths.removeBody(displayName(removing.rel_path) || displayName(rootName(removing.root_id))) : ''}
  confirm={t.admin.paths.remove}
  error={removeError}
  onconfirm={remove}
  oncancel={() => (removing = null)}
/>

<style>
  .muted,
  .none {
    color: var(--ink-2);
  }
  .group + .group {
    margin-top: 10px;
    padding-top: 10px;
    border-top: 1px solid var(--hair);
  }
  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
  }
  h3 {
    margin: 0;
    font-size: 14px;
    font-weight: 600;
    overflow-wrap: anywhere;
  }
  .none {
    margin: 0;
    font-size: 13px;
  }
  ul {
    list-style: none;
    margin: 2px 0 0;
    padding: 0 0 0 12px;
    border-left: 2px solid var(--hair);
  }
  li {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 2px 0;
  }
  .what {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 4px 8px;
  }
  .p {
    overflow-wrap: anywhere;
    min-width: 0;
  }
  .whole {
    font-weight: 500;
  }
  form {
    margin-top: 8px;
    padding: 10px;
    border-radius: 6px;
    background: var(--sunk);
  }
  .input {
    display: flex;
    align-items: center;
    padding-left: 9px;
    border: var(--edge);
    border-radius: 6px;
    background: var(--paper);
  }
  .input:focus-within {
    box-shadow: var(--focus);
  }
  .input.bad {
    border-color: var(--danger);
  }
  .input > span {
    color: var(--ink-2);
  }
  input,
  .browse {
    border: 0;
    background: transparent;
    color: inherit;
    font: inherit;
    outline: none;
  }
  /* Width 0 so the input's default width cannot push the card past its column; the wrapper draws the focus ring. */
  input {
    flex: 1;
    width: 0;
    padding: 7px 4px;
  }
  input:focus-visible {
    box-shadow: none;
  }
  .browse {
    align-self: stretch;
    padding: 7px 10px;
    border-left: 1px solid var(--hair);
    border-radius: 0 5px 5px 0;
    font-weight: 500;
    cursor: pointer;
  }
  .browse:hover,
  .browse:focus-visible {
    background: var(--sunk);
  }
  .hint,
  .err {
    margin: 6px 0 0;
    font-size: 12px;
  }
  .hint {
    color: var(--ink-2);
  }
  .err {
    color: var(--danger);
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
    margin-top: 10px;
  }
  .grow {
    flex: 1;
  }
</style>
