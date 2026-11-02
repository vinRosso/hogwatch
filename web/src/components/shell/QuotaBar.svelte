<script lang="ts">
  import { formatBytes } from '../../lib/format'
  import { t } from '../../lib/i18n/en'
  import { session } from '../../lib/session.svelte'
  import Pill from '../ui/Pill.svelte'

  let { big = false }: { big?: boolean } = $props()
  const q = $derived(session.quota())
</script>

<div class={['quota', { big }]}>
  {#if q}
    <div
      class="track"
      role="progressbar"
      aria-label={t.quota.label}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={Math.min(q.pct, 100)}
    >
      <div class={['fill', q.level]} style:width={`${Math.min(q.pct, 100)}%`}></div>
      <span class="tick" style:left="80%"></span>
      <span class="tick" style:left="95%"></span>
    </div>
    <span class="fig mono">{t.quota.of(formatBytes(q.used), formatBytes(q.limit))}</span>
    {#if q.level === 'warn95'}<Pill tone="danger">{t.quota.warn95}</Pill>
    {:else if q.level === 'warn80'}<Pill tone="warn">{t.quota.warn80}</Pill>{/if}
  {:else}
    <span class="fig">{t.quota.none}</span>
  {/if}
</div>

<style>
  .quota {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .track {
    position: relative;
    width: 160px;
    height: 12px;
    border: var(--edge);
    border-radius: var(--r-pill);
    background: var(--card);
    overflow: hidden;
  }
  .big .track {
    width: 100%;
    max-width: 520px;
    height: 18px;
  }
  .fill {
    height: 100%;
    background: var(--sage);
  }
  .fill.warn80 {
    background: var(--mustard);
  }
  .fill.warn95 {
    background: var(--danger);
  }
  .tick {
    position: absolute;
    top: 0;
    bottom: 0;
    width: 1px;
    background: var(--ink);
  }
  .fig {
    font-size: 12px;
    white-space: nowrap;
  }
</style>
