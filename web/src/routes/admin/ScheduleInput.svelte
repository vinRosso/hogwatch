<script lang="ts">
  import { untrack } from 'svelte'
  import { t } from '../../lib/i18n/en'
  import { describeCron, parseCron, type Freq } from '../../lib/schedule'

  // `value` is the five-field cron the server stores; an empty string means the root is scanned only on demand.
  let { value = $bindable('') }: { value: string } = $props()

  const weekdays = t.admin.roots.weekdays
  const days = Array.from({ length: 28 }, (_, i) => i + 1)

  // The cron for the current controls, or null while the time field is empty.
  function build(): string | null {
    if (freq === 'manual') return ''
    if (freq === 'custom') return custom.trim()
    const m = /^(\d{2}):(\d{2})$/.exec(time)
    if (!m) return null
    const at = `${Number(m[2])} ${Number(m[1])}`
    if (freq === 'nightly') return `${at} * * *`
    if (freq === 'weekly') return `${at} * * ${day}`
    return `${at} ${dom} * *`
  }

  const init = untrack(() => parseCron(value))
  let freq = $state<Freq>(init.freq)
  let time = $state(init.time)
  let day = $state(init.day)
  let dom = $state(init.dom)
  let custom = $state(untrack(() => value))

  // Controls write the cron...
  $effect(() => {
    const next = build()
    if (next !== null) untrack(() => { if (next !== value) value = next })
  })

  // ...and an outside change to the cron (a loaded root) refills the controls.
  $effect(() => {
    const v = value
    untrack(() => {
      if (v === build()) return
      const p = parseCron(v)
      freq = p.freq
      time = p.time
      day = p.day
      dom = p.dom
      custom = v
    })
  })

  const summary = $derived(freq === 'custom' ? t.admin.roots.schedCronHint : describeCron(value))
</script>

<div class="sched">
  <span class="lab">{t.admin.roots.schedule}</span>
  <div class="row">
    <label class="ctl">
      <span class="sub">{t.admin.roots.schedFreq}</span>
      <select bind:value={freq}>
        <option value="manual">{t.admin.roots.schedManual}</option>
        <option value="nightly">{t.admin.roots.schedNightly}</option>
        <option value="weekly">{t.admin.roots.schedWeekly}</option>
        <option value="monthly">{t.admin.roots.schedMonthly}</option>
        <option value="custom">{t.admin.roots.schedCustom}</option>
      </select>
    </label>
    {#if freq === 'custom'}
      <label class="ctl wide">
        <span class="sub">{t.admin.roots.schedCron}</span>
        <input class="mono" bind:value={custom} autocomplete="off" spellcheck="false" />
      </label>
    {:else if freq === 'weekly'}
      <label class="ctl">
        <span class="sub">{t.admin.roots.schedDay}</span>
        <select bind:value={day}>
          {#each weekdays as name, i (i)}<option value={i}>{name}</option>{/each}
        </select>
      </label>
    {:else if freq === 'monthly'}
      <label class="ctl">
        <span class="sub">{t.admin.roots.schedDom}</span>
        <select bind:value={dom}>
          {#each days as n (n)}<option value={n}>{n}</option>{/each}
        </select>
      </label>
    {/if}
    {#if freq === 'nightly' || freq === 'weekly' || freq === 'monthly'}
      <label class="ctl">
        <span class="sub">{t.admin.roots.schedTime}</span>
        <input type="time" bind:value={time} />
      </label>
    {/if}
  </div>
  <p class="hint">{summary}</p>
</div>

<style>
  .sched {
    display: grid;
    gap: 4px;
    margin-bottom: 12px;
  }
  .lab {
    font-weight: 500;
  }
  .sub {
    color: var(--ink-2);
    font-size: 12px;
  }
  .row {
    display: flex;
    gap: 10px;
    align-items: flex-end;
    flex-wrap: wrap;
  }
  .ctl {
    display: grid;
    justify-items: start;
    gap: 4px;
    margin-top: 4px;
  }
  .ctl.wide {
    flex: 1 1 220px;
  }
  select,
  input {
    min-width: 0;
    height: 34px;
    padding: 6px 8px;
    border: var(--edge);
    border-radius: 6px;
    background: var(--paper);
    color: var(--ink);
    font-size: 14px;
  }
  input.mono {
    width: 100%;
  }
  .hint {
    margin: 0;
    color: var(--ink-2);
    font-size: 12px;
  }
</style>
