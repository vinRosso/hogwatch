<script lang="ts">
  import { untrack } from 'svelte'
  import { api, ApiError } from '../../lib/api/client'
  import type { ListingCreated, ListingRequest } from '../../lib/api/types'
  import { parseBytes } from '../../lib/format'
  import { t } from '../../lib/i18n/en'
  import { rememberListing } from '../../lib/listings'
  import { navigate } from '../../lib/router.svelte'
  import Button from '../ui/Button.svelte'
  import Dialog from '../ui/Dialog.svelte'
  import Field from '../ui/Field.svelte'

  // initialMode is the mode the dialog opens with; the user can still change it.
  let { open, ref, initialMode = 'children', onclose }: { open: boolean; ref: string; initialMode?: 'children' | 'subtree'; onclose: () => void } = $props()

  let mode = $state<'children' | 'subtree'>(untrack(() => initialMode))
  $effect(() => {
    if (open) mode = initialMode
  })
  let exts = $state('')
  let minSize = $state('')
  let minAge = $state('')
  let submitting = $state(false)
  let formError = $state<string | null>(null)

  const extList = $derived(
    exts
      .split(',')
      .map((s) => s.trim())
      .filter((s) => s !== ''),
  )
  const extError = $derived(extList.length > 20 || extList.some((e) => !/^[a-z0-9]{1,32}$/.test(e)) ? t.listing.extBad : null)
  const sizeValue = $derived(minSize.trim() === '' ? undefined : parseBytes(minSize))
  const sizeError = $derived(sizeValue === null ? t.listing.sizeBad : null)
  const ageValue = $derived(minAge.trim() === '' ? undefined : /^\d+$/.test(minAge.trim()) ? parseInt(minAge.trim(), 10) : null)
  const ageError = $derived(ageValue === null || (ageValue !== undefined && ageValue > 36500) ? t.listing.ageBad : null)
  const invalid = $derived(!!(extError || sizeError || ageError))

  async function submit(e: Event) {
    e.preventDefault()
    if (invalid || submitting) return
    submitting = true
    formError = null
    const body: ListingRequest = { ref, mode }
    if (extList.length) body.exts = extList
    if (sizeValue !== undefined && sizeValue !== null) body.min_size = sizeValue
    if (ageValue !== undefined && ageValue !== null) body.min_age_days = ageValue
    try {
      const r = await api.post<ListingCreated>('/listings', body)
      rememberListing(r.id)
      onclose()
      navigate(`/listings/${r.id}`)
    } catch (err) {
      formError = err instanceof ApiError && err.status === 429 ? t.listing.tooMany : t.listing.failed
    } finally {
      submitting = false
    }
  }
</script>

<Dialog {open} title={t.listing.title} {onclose}>
  <form onsubmit={submit} novalidate>
    <fieldset>
      <legend>{t.listing.mode}</legend>
      <label><input type="radio" bind:group={mode} value="children" /> {t.listing.children}</label>
      <label><input type="radio" bind:group={mode} value="subtree" /> {t.listing.subtree}</label>
    </fieldset>
    <Field label={t.listing.exts} hint={t.listing.extsHint} error={extError}>
      <input bind:value={exts} autocomplete="off" spellcheck="false" />
    </Field>
    <Field label={t.listing.minSize} hint={t.listing.minSizeHint} error={sizeError}>
      <input bind:value={minSize} autocomplete="off" />
    </Field>
    <Field label={t.listing.minAge} error={ageError}>
      <input bind:value={minAge} inputmode="numeric" autocomplete="off" />
    </Field>
    {#if formError}<p class="err" role="alert">{formError}</p>{/if}
    <div class="row">
      <Button variant="ghost" type="button" onclick={onclose}>{t.common.cancel}</Button>
      <Button type="submit" disabled={invalid || submitting}>{t.listing.submit}</Button>
    </div>
  </form>
</Dialog>

<style>
  fieldset {
    border: none;
    padding: 0;
    margin: 0 0 12px;
    display: grid;
    gap: 4px;
  }
  legend {
    font-weight: 500;
    padding: 0;
    margin-bottom: 4px;
  }
  .err {
    color: var(--danger);
  }
  .row {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
  }
</style>
