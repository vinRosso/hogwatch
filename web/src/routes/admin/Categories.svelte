<script lang="ts">
  import { onMount } from 'svelte'
  import { api } from '../../lib/api/client'
  import type { components } from '../../lib/api/schema'
  import { problemText } from '../../lib/errors'
  import { t } from '../../lib/i18n/en'
  import Button from '../../components/ui/Button.svelte'
  import EmptyState from '../../components/ui/EmptyState.svelte'

  type Cat = components['schemas']['Category']
  const names = Object.keys(t.filter.cat) as Cat['category'][]
  const swatch: Record<string, string> = {
    video: 'var(--plum)',
    audio: 'var(--rose)',
    image: 'var(--teal)',
    archive: 'var(--mustard)',
    'disk-image': 'var(--sky)',
    document: 'var(--sage)',
    code: 'var(--terra)',
    other: 'var(--ink-3)',
  }

  let items = $state<Cat[] | null>(null)
  let failed = $state(false)
  let error = $state<string | null>(null)
  let ext = $state('')
  let category = $state<Cat['category']>('other')
  // The extension being moved, by drag or by select-then-choose (the single-pointer and keyboard path).
  let held = $state<Cat | null>(null)
  let dragging = $state(false)
  let over = $state<Cat['category'] | null>(null)
  const extError = $derived(ext !== '' && !/^[a-z0-9]{1,32}$/.test(ext) ? t.admin.categories.extBad : null)
  // The field doubles as the filter; an extension already listed is moved by dragging, not from the form.
  const existing = $derived(items?.find((c) => c.ext === ext) ?? null)
  const shown = $derived((items ?? []).filter((c) => c.ext.includes(ext)))
  const groups = $derived(names.map((n) => ({ name: n, exts: shown.filter((c) => c.category === n) })))
  const used = $derived(new Set((items ?? []).map((c) => c.category)).size)

  async function load() {
    failed = false
    try {
      items = (await api.get<{ items: Cat[] }>('/admin/categories')).items
    } catch {
      failed = true
    }
  }
  onMount(() => void load())

  async function put(e: string, c: Cat['category']): Promise<boolean> {
    error = null
    try {
      await api.put(`/admin/categories/${e}`, { category: c })
      await load()
      return true
    } catch (err) {
      error = problemText(err, t.admin.failed)
      await load()
      return false
    }
  }

  async function remove(e: string) {
    error = null
    try {
      await api.del(`/admin/categories/${e}`)
      await load()
    } catch (err) {
      error = problemText(err, t.admin.failed)
    }
  }

  function move(to: Cat['category']) {
    const c = held
    held = null
    dragging = false
    over = null
    if (c && c.category !== to) void put(c.ext, to)
  }

  function onDragStart(e: DragEvent, c: Cat) {
    held = c
    dragging = true
    e.dataTransfer?.setData('text/plain', c.ext)
    if (e.dataTransfer) e.dataTransfer.effectAllowed = 'move'
  }

  function onDragEnd() {
    // A drop already cleared these; a cancelled drag lands here alone.
    held = null
    dragging = false
    over = null
  }

  function onDragOver(e: DragEvent, to: Cat['category']) {
    if (!held || held.category === to) return
    e.preventDefault()
    if (e.dataTransfer) e.dataTransfer.dropEffect = 'move'
    over = to
  }
</script>

<svelte:window onkeydown={(e) => e.key === 'Escape' && held && !dragging && (held = null)} />

<section>
  <header>
    <h1>{t.admin.sections.categories}</h1>
    <p class="lead">{t.admin.categories.lead}</p>
  </header>

  <form
    class="bar"
    onsubmit={(e) => {
      e.preventDefault()
      if (ext !== '' && !extError && !existing) void put(ext, category).then((ok) => ok && (ext = ''))
    }}
  >
    <label class="field">
      <span>{t.admin.categories.ext}</span>
      <input
        bind:value={ext}
        class="mono"
        autocomplete="off"
        spellcheck="false"
        aria-invalid={!!extError}
        aria-describedby="cat-hint"
      />
    </label>
    <label class="field">
      <span>{t.admin.categories.category}</span>
      <select bind:value={category}>
        {#each names as n (n)}<option value={n}>{t.filter.cat[n]}</option>{/each}
      </select>
    </label>
    <Button type="submit" disabled={ext === '' || !!extError || !!existing}>{t.admin.categories.add}</Button>
    <p id="cat-hint" class={['hint', extError && 'err']} role={extError ? 'alert' : undefined}>
      {#if extError}{extError}
      {:else if existing}{t.admin.categories.now(t.filter.cat[existing.category])}
      {:else}{t.admin.categories.hint}{/if}
    </p>
  </form>
  {#if error}<p class="err" role="alert">{error}</p>{/if}

  {#if failed}
    <EmptyState title={t.common.error} body={t.admin.categories.failed}>
      <Button onclick={load}>{t.common.retry}</Button>
    </EmptyState>
  {:else if items === null}
    <p>{t.common.loading}</p>
  {:else if items.length === 0}
    <EmptyState title={t.admin.sections.categories} body={t.admin.categories.empty} />
  {:else}
    <p class="summary" role="status">
      {#if held && !dragging}
        {t.admin.categories.held(held.ext)}
        <button class="link" type="button" onclick={() => (held = null)}>{t.admin.categories.cancel}</button>
      {:else if shown.length === 0}{t.admin.categories.noMatch}
      {:else}{t.admin.categories.summary(items.length, used)}{/if}
    </p>
    <div class={['grid', held && 'moving']}>
      {#each groups as g (g.name)}
        {#if ext === '' || held || g.exts.length > 0}
          <section
            class={['group', over === g.name && 'over', held?.category === g.name && 'from']}
            style:--sw={swatch[g.name]}
            role="group"
            aria-label={t.filter.cat[g.name]}
            ondragover={(e) => onDragOver(e, g.name)}
            ondragleave={(e) => {
              if (!(e.currentTarget as Node).contains(e.relatedTarget as Node | null)) over = null
            }}
            ondrop={(e) => {
              e.preventDefault()
              move(g.name)
            }}
          >
            <h2>
              <span class="dot" aria-hidden="true"></span>
              {t.filter.cat[g.name]}
              {#if held && !dragging && held.category !== g.name}
                <button class="here" type="button" onclick={() => move(g.name)}>{t.admin.categories.here}</button>
              {:else}
                <span class="count">{g.exts.length}</span>
              {/if}
            </h2>
            {#if g.exts.length === 0}
              <p class="none">{t.admin.categories.noneIn}</p>
            {:else}
              <ul>
                {#each g.exts as c (c.ext)}
                  <li
                    class={['chip', (c.ext === ext || held?.ext === c.ext) && 'on', dragging && held?.ext === c.ext && 'ghost']}
                    draggable="true"
                    ondragstart={(e) => onDragStart(e, c)}
                    ondragend={onDragEnd}
                  >
                    <button
                      class="name"
                      type="button"
                      title={t.admin.categories.edit(c.ext)}
                      aria-pressed={held?.ext === c.ext}
                      onclick={() => (held = held?.ext === c.ext ? null : c)}
                    >
                      <span style="unicode-bidi: isolate">{c.ext}</span>
                    </button>
                    <button
                      class="x"
                      type="button"
                      aria-label={t.admin.categories.drop(c.ext)}
                      title={t.admin.categories.remove}
                      onclick={() => remove(c.ext)}>×</button
                    >
                  </li>
                {/each}
              </ul>
            {/if}
          </section>
        {/if}
      {/each}
    </div>
  {/if}
</section>

<style>
  header {
    margin-bottom: 16px;
  }
  h1 {
    font: 600 24px var(--serif);
    margin: 0 0 4px;
  }
  .lead {
    color: var(--ink-2);
    margin: 0;
    max-width: 65ch;
  }
  .bar {
    display: flex;
    flex-wrap: wrap;
    align-items: flex-end;
    gap: 8px 12px;
    background: var(--card);
    border: var(--edge);
    border-radius: var(--r);
    box-shadow: var(--pop);
    padding: 14px 16px;
    margin-bottom: 16px;
  }
  .field {
    display: flex;
    flex-direction: column;
    gap: 4px;
    font-size: 12px;
    color: var(--ink-2);
    font-weight: 500;
  }
  .field input,
  .field select {
    border: var(--edge);
    border-radius: 6px;
    padding: 6px 8px;
    background: var(--paper);
    color: var(--ink);
    font-size: 14px;
    min-height: 34px;
  }
  .field input {
    width: 14ch;
  }
  .field input[aria-invalid='true'] {
    border-color: var(--danger);
  }
  .hint {
    flex-basis: 100%;
    margin: 0;
    font-size: 12px;
    color: var(--ink-2);
  }
  .summary {
    color: var(--ink-2);
    font-size: 12px;
    margin: 0 0 8px;
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
    gap: 12px;
    align-items: start;
  }
  .group {
    background: var(--card);
    border: var(--edge);
    border-radius: var(--r);
    box-shadow: 3px 3px 0 var(--sw);
    padding: 10px 12px 12px;
    transition:
      box-shadow 120ms ease-out,
      transform 120ms ease-out;
  }
  .moving .group:not(.from) {
    border-style: dashed;
  }
  .group.over {
    border-style: solid;
    box-shadow: 6px 6px 0 var(--sw);
    transform: translate(-2px, -2px);
    background: var(--sunk);
  }
  .here,
  .link {
    border: 0;
    background: none;
    color: var(--terra);
    font: 500 12px var(--sans);
    text-decoration: underline;
    cursor: pointer;
    padding: 2px 0;
  }
  .here {
    margin-left: auto;
  }
  .link {
    margin-left: 6px;
  }
  h2 {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 15px;
    margin: 0 0 10px;
  }
  .dot {
    width: 10px;
    height: 10px;
    border-radius: 50%;
    background: var(--sw);
    border: 1px solid var(--ink);
  }
  .count {
    margin-left: auto;
    font: 500 12px var(--mono);
    color: var(--ink-2);
    font-variant-numeric: tabular-nums;
  }
  ul {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .chip {
    display: inline-flex;
    align-items: stretch;
    border: 1px solid var(--hair);
    border-radius: var(--r-pill);
    background: var(--paper);
    overflow: hidden;
    cursor: grab;
  }
  .chip.on {
    border-color: var(--ink);
    background: var(--sunk);
  }
  .chip.ghost {
    opacity: 0.4;
  }
  .chip button {
    border: 0;
    background: none;
    color: var(--ink);
    cursor: inherit;
    min-height: 26px;
  }
  .chip button:focus-visible {
    outline: none;
    box-shadow: inset 0 0 0 2px var(--terra);
  }
  .name {
    font: 13px var(--mono);
    padding: 2px 4px 2px 10px;
  }
  .name:hover {
    background: var(--sunk);
  }
  .x {
    padding: 0 8px 0 4px;
    color: var(--ink-3) !important;
    cursor: pointer !important;
    font-size: 15px;
    line-height: 1;
  }
  .x:hover {
    color: var(--danger) !important;
    background: var(--danger-bg);
  }
  .none {
    margin: 0;
    color: var(--ink-3);
    font-size: 13px;
  }
  .err {
    color: var(--danger);
  }
</style>
