<script lang="ts">
  import { ApiError } from '../../lib/api/client'
  import { startExport, type ExportFormat, type ExportWhat } from '../../lib/export'
  import { problemText } from '../../lib/errors'
  import { t } from '../../lib/i18n/en'
  import { toast } from '../../lib/toast.svelte'
  import Button from '../ui/Button.svelte'

  let { ref }: { ref: string } = $props()
  let open = $state(false)
  let busy = $state(false)
  const whats: ExportWhat[] = ['tree', 'top', 'usage']
  const formats: ExportFormat[] = ['csv', 'json']

  async function go(format: ExportFormat, what: ExportWhat) {
    busy = true
    try {
      const r = await startExport(ref, format, what)
      if (r === 'started') open = false
      else toast(t.export.busy(r.retryAfter), 'error')
    } catch (e) {
      toast(problemText(e, t.export.failed), 'error')
    } finally {
      busy = false
    }
  }
</script>

<svelte:window onkeydown={(e) => open && e.key === 'Escape' && (open = false)} />

<div class="wrap">
  <Button variant="ghost" aria-expanded={open} onclick={() => (open = !open)}>{t.export.button}</Button>
  {#if open}
    <div class="menu" role="group" aria-label={t.export.menu}>
      {#each whats as w (w)}
        <div class="row">
          <span>{t.export[w]}</span>
          {#each formats as f (f)}
            <button disabled={busy} aria-label={`${t.export[w]} ${t.export[f]}`} onclick={() => go(f, w)}>{t.export[f]}</button>
          {/each}
        </div>
      {/each}
    </div>
  {/if}
</div>

<style>
  .wrap {
    position: relative;
  }
  .menu {
    position: absolute;
    right: 0;
    top: calc(100% + 4px);
    z-index: 20;
    min-width: 220px;
    background: var(--card);
    border: var(--edge);
    border-radius: var(--r);
    box-shadow: 3px 3px 0 var(--ink);
    padding: 6px;
  }
  .row {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 4px;
  }
  .row span {
    flex: 1;
  }
  .row button {
    border: var(--edge);
    border-radius: var(--r-pill);
    background: var(--paper);
    padding: 2px 10px;
    cursor: pointer;
  }
</style>
