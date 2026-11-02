<script lang="ts">
  import { api } from '../../lib/api/client'
  import { t } from '../../lib/i18n/en'
  import { session } from '../../lib/session.svelte'
  import Button from '../ui/Button.svelte'

  const text = $derived(session.me?.notice ? (t.profile.notice[session.me.notice] ?? null) : null)

  async function dismiss() {
    await api.del('/me/notice')
    if (session.me) session.me.notice = null
  }
</script>

{#if text}
  <div class="notice" role="status">
    <span>{text}</span>
    <Button variant="ghost" onclick={dismiss}>{t.common.dismiss}</Button>
  </div>
{/if}

<style>
  .notice {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    background: var(--mustard-bg);
    border-bottom: var(--edge);
    padding: 6px 20px;
  }
</style>
