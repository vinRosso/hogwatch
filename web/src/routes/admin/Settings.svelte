<script lang="ts">
  import { onMount } from 'svelte'
  import { api, ApiError } from '../../lib/api/client'
  import { problemText } from '../../lib/errors'
  import { t } from '../../lib/i18n/en'
  import { toast } from '../../lib/toast.svelte'
  import Button from '../../components/ui/Button.svelte'
  import EmptyState from '../../components/ui/EmptyState.svelte'
  import Field from '../../components/ui/Field.svelte'

  type Map = Record<string, string>
  let saved = $state<Map | null>(null)
  let form = $state<Map>({})
  let failed = $state(false)
  let error = $state<string | null>(null)

  async function load() {
    failed = false
    try {
      saved = await api.get<Map>('/admin/settings')
      form = { ...saved }
    } catch {
      failed = true
    }
  }
  onMount(() => void load())

  const changed = $derived(saved ? Object.keys(form).filter((k) => form[k] !== saved![k]) : [])

  async function save(e: Event) {
    e.preventDefault()
    error = null
    try {
      // PUT replaces the whole map, so every key is sent, not only the changed ones.
      saved = await api.put<Map>('/admin/settings', { ...form })
      form = { ...saved }
      toast(t.admin.saved, 'saved')
    } catch (err) {
      error = problemText(err, t.admin.failed)
    }
  }
</script>

<section>
  <h1>{t.admin.sections.settings}</h1>
  {#if failed}
    <EmptyState title={t.common.error} body={t.admin.settings.failed}>
      <Button onclick={load}>{t.common.retry}</Button>
    </EmptyState>
  {:else if saved === null}
    <p>{t.common.loading}</p>
  {:else if Object.keys(form).length === 0}
    <EmptyState title={t.admin.sections.settings} body={t.admin.settings.none} />
  {:else}
    <form onsubmit={save} novalidate>
      {#each Object.keys(form).sort() as k (k)}
        <Field label={k}><input bind:value={form[k]} autocomplete="off" spellcheck="false" /></Field>
      {/each}
      {#if changed.length > 0}
        <div class="changes" role="status">
          <strong>{t.admin.settings.changed}</strong>
          <ul>
            {#each changed as k (k)}
              <li><span class="mono">{k}</span>: <span class="mono">{saved[k]}</span> → <span class="mono">{form[k]}</span></li>
            {/each}
          </ul>
        </div>
      {/if}
      {#if error}<p class="err" role="alert">{error}</p>{/if}
      <Button type="submit" disabled={changed.length === 0}>{t.common.save}</Button>
    </form>
  {/if}
</section>

<style>
  section {
    max-width: 640px;
  }
  h1 {
    font: 600 24px var(--serif);
    margin: 0 0 12px;
  }
  .changes {
    background: var(--mustard-bg);
    border: var(--edge);
    border-radius: 6px;
    padding: 8px 12px;
    margin-bottom: 12px;
  }
  .changes ul {
    margin: 4px 0 0;
    padding-left: 18px;
  }
  .err {
    color: var(--danger);
  }
</style>
