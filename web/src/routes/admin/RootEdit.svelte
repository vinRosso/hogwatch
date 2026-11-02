<script lang="ts">
  import { onMount } from 'svelte'
  import { api, ApiError } from '../../lib/api/client'
  import type { AdminRoot, RootPatch } from '../../lib/api/types'
  import { displayName } from '../../lib/fname'
  import ScheduleInput from './ScheduleInput.svelte'
  import { problemText } from '../../lib/errors'
  import { t } from '../../lib/i18n/en'
  import { diffPatch } from '../../lib/patch'
  import { navigate } from '../../lib/router.svelte'
  import { toast } from '../../lib/toast.svelte'
  import Button from '../../components/ui/Button.svelte'
  import Card from '../../components/ui/Card.svelte'
  import Confirm from '../../components/ui/Confirm.svelte'
  import EmptyState from '../../components/ui/EmptyState.svelte'
  import Field from '../../components/ui/Field.svelte'
  import RootName from '../../components/ui/RootName.svelte'

  let { id }: { id: number } = $props()

  type Form = { name: string; cross_fs: boolean; schedule: string; enabled: boolean; excludes: string }
  const keys: (keyof Form)[] = ['name', 'cross_fs', 'schedule', 'enabled', 'excludes']

  let root = $state<AdminRoot | null>(null)
  let before = $state<Form | null>(null)
  let form = $state<Form>({ name: '', cross_fs: false, schedule: '', enabled: true, excludes: '' })
  let failed = $state(false)
  let error = $state<string | null>(null)
  let removing = $state(false)
  let removeError = $state<string | null>(null)

  function adopt(r: AdminRoot) {
    root = r
    const f: Form = { name: r.name, cross_fs: r.cross_fs, schedule: r.schedule, enabled: r.enabled, excludes: r.excludes.join('\n') }
    before = { ...f }
    form = { ...f }
  }

  async function load() {
    failed = false
    try {
      const found = (await api.get<{ items: AdminRoot[] }>('/admin/roots')).items.find((r) => r.id === id)
      if (found) adopt(found)
      else failed = true
    } catch {
      failed = true
    }
  }
  onMount(() => void load())

  async function save(e: Event) {
    e.preventDefault()
    if (!before) return
    error = null
    const changed = diffPatch(before, form, keys)
    if (Object.keys(changed).length === 0) return
    // The contract takes excludes as a list; the form edits them one per line.
    const { excludes, ...rest } = changed
    const body: RootPatch = { ...rest }
    if (excludes !== undefined) body.excludes = excludes.split('\n').map((s) => s.trim()).filter((s) => s !== '')
    try {
      adopt(await api.patch<AdminRoot>(`/admin/roots/${id}`, body))
      toast(t.admin.saved, 'saved')
    } catch (err) {
      error = problemText(err, t.admin.failed)
    }
  }

  async function remove() {
    removeError = null
    try {
      await api.del(`/admin/roots/${id}`)
      removing = false
      navigate('/admin/mounts')
    } catch (err) {
      removeError = problemText(err, t.admin.failed)
    }
  }
</script>

<section>
  <p><a href="/admin/mounts">← {t.admin.roots.back}</a></p>
  {#if failed}
    <EmptyState title={t.common.error} body={t.admin.roots.loadFailed}>
      <Button onclick={load}>{t.common.retry}</Button>
    </EmptyState>
  {:else if !root}
    <p>{t.common.loading}</p>
  {:else}
    <form onsubmit={save} novalidate class:off={!form.enabled}>
      <Card>
        <div class="head">
          <div>
            <h1><RootName id={root.id} name={root.name} /></h1>
            <p class="mono path" style="unicode-bidi: isolate">{displayName(root.container_path)} → {displayName(root.host_path)}</p>
          </div>
          <div class="actions">
            <button
              type="button"
              class={['toggle', !form.enabled && 'off']}
              aria-pressed={!form.enabled}
              onclick={() => (form.enabled = !form.enabled)}
            >{form.enabled ? t.admin.roots.enabled : t.admin.roots.disabled}</button>
            <Button type="submit">{t.common.save}</Button>
          </div>
        </div>
        <Field label={t.admin.roots.name}><input bind:value={form.name} autocomplete="off" /></Field>
        <ScheduleInput bind:value={form.schedule} />
        <Field label={t.admin.roots.excludes}><textarea rows="4" bind:value={form.excludes} spellcheck="false"></textarea></Field>
        <label class="chk"><input type="checkbox" bind:checked={form.cross_fs} /> {t.admin.roots.crossFs}</label>
        {#if error}<p class="err" role="alert">{error}</p>{/if}
      </Card>
    </form>
    <div><Button variant="danger" onclick={() => ((removing = true), (removeError = null))}>{t.admin.roots.remove}</Button></div>
  {/if}
</section>

<Confirm
  open={removing}
  title={t.admin.roots.remove}
  message={t.admin.roots.removeBody(root ? displayName(root.name) : '')}
  confirm={t.admin.roots.remove}
  error={removeError}
  onconfirm={remove}
  oncancel={() => (removing = false)}
/>

<style>
  section {
    display: grid;
    gap: 12px;
    max-width: 720px;
  }
  h1 {
    font: 600 24px var(--serif);
    margin: 0;
  }
  .path {
    margin: 0;
    color: var(--ink-2);
    overflow-wrap: anywhere;
  }
  .head {
    display: flex;
    flex-wrap: wrap;
    align-items: flex-start;
    justify-content: space-between;
    gap: 8px 16px;
    margin-bottom: 12px;
  }
  .actions {
    display: flex;
    gap: 8px;
    margin-left: auto;
  }
  /* Same look as the enable toggle on the user edit page. */
  .toggle {
    height: 34px;
    border: var(--edge);
    border-radius: var(--r-pill);
    padding: 0 14px;
    font: 500 12px var(--sans, inherit);
    color: var(--ink);
    background: var(--card);
    cursor: pointer;
  }
  .toggle.off {
    background: var(--danger);
    border-color: var(--danger);
    color: var(--paper);
  }
  .off :global(.card) {
    background-image: repeating-linear-gradient(-45deg, transparent 0 6px, color-mix(in srgb, var(--ink-2) 14%, transparent) 6px 7px);
  }
  .chk {
    display: block;
    margin-bottom: 8px;
  }
  .err {
    color: var(--danger);
  }
</style>
