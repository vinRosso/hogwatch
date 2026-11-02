<script lang="ts">
  import { formatBytes } from '../../lib/format'
  import { t } from '../../lib/i18n/en'

  /** Growth in bytes: + in danger colour, − in sage, null (no history point) as a dash. */
  let { bytes }: { bytes: number | null } = $props()
</script>

{#if bytes === null}
  <span class="g mono nil" title={t.growth.none}>{t.common.none}</span>
{:else if bytes > 0}
  <span class="g mono up">+{formatBytes(bytes)}</span>
{:else if bytes < 0}
  <span class="g mono dn">−{formatBytes(-bytes)}</span>
{:else}
  <span class="g mono">{formatBytes(0)}</span>
{/if}

<style>
  .up {
    color: var(--danger);
  }
  .dn {
    color: var(--sage);
  }
  .nil {
    color: var(--ink-3);
  }
</style>
