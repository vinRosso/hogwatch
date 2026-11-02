<script lang="ts">
  import { untrack } from 'svelte'
  import Field from '../../components/ui/Field.svelte'
  import { formatBytes, parseBytes } from '../../lib/format'
  import { t } from '../../lib/i18n/en'

  // `usage` is the user's current total; leave it null when it is not known.
  let { value = $bindable(), invalid = $bindable(false), usage = null }: { value: number | null; invalid?: boolean; usage?: number | null } = $props()

  const units = [
    { name: 'MB', size: 1e6 },
    { name: 'GB', size: 1e9 },
    { name: 'TB', size: 1e12 },
    { name: 'PB', size: 1e15 },
  ]

  function unitFor(bytes: number | null): (typeof units)[number] {
    if (!bytes) return units[1]
    return bytes >= 1e15 ? units[3] : bytes >= 1e12 ? units[2] : bytes >= 1e9 ? units[1] : units[0]
  }

  // The amount stays exact, and value only changes on an edit, so an untouched quota is saved as it was.
  const initial = untrack(() => value)
  let unit = $state(unitFor(initial).name)
  let amount = $state(initial === null ? '' : String(initial / unitFor(initial).size))

  function edit() {
    const parsed = amount.trim() === '' ? null : parseBytes(`${amount} ${unit}`)
    invalid = amount.trim() !== '' && parsed === null
    value = parsed
  }

  const pct = $derived(usage !== null && value !== null && value > 0 ? Math.round((usage / value) * 100) : null)
</script>

<Field label={t.admin.users.quota} hint={t.admin.users.quotaHint} error={invalid ? t.admin.users.quotaBad : null}>
  <span class="row">
    <input aria-label={t.admin.users.quotaAmount} placeholder={t.quota.none} inputmode="decimal" autocomplete="off" maxlength={9} bind:value={amount} oninput={edit} />
    <select aria-label={t.admin.users.quotaUnit} bind:value={unit} onchange={edit}>
      {#each units as u (u.name)}<option value={u.name}>{u.name}</option>{/each}
    </select>
  </span>
</Field>
{#if usage !== null && !invalid}
  {#if value === null}
    <p class="use">{t.admin.users.quotaNoneUses(formatBytes(usage))}</p>
  {:else if pct !== null}
    <span class="meter" role="progressbar" aria-label={t.admin.ranking.quotaPct} aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.min(100, pct)}>
      <span class={['fill', pct >= 95 ? 'hi' : pct >= 80 ? 'mid' : '']} style:width={`${Math.min(100, pct)}%`}></span>
    </span>
    <p class="use">{t.admin.users.quotaUses(formatBytes(usage), pct, formatBytes(value * 0.8), formatBytes(value * 0.95))}</p>
  {/if}
{/if}

<style>
  .row {
    display: flex;
    gap: 8px;
  }
  .row input {
    box-sizing: content-box;
    width: 9ch;
  }
  .meter {
    display: block;
    height: 10px;
    margin-bottom: 6px;
    border: var(--edge);
    border-radius: var(--r-pill);
    background: var(--card);
    overflow: hidden;
  }
  .fill {
    display: block;
    height: 100%;
    background: var(--sage);
  }
  .fill.mid {
    background: var(--mustard);
  }
  .fill.hi {
    background: var(--danger);
  }
  .use {
    margin: 0 0 12px;
    color: var(--ink-2);
    font-size: 13px;
  }
</style>
