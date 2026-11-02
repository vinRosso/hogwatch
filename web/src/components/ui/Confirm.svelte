<script lang="ts">
  import { t } from '../../lib/i18n/en'
  import Button from './Button.svelte'
  import Dialog from './Dialog.svelte'

  /** A confirmation that names its target. `error` shows the server's refusal and keeps the dialog open. */
  let {
    open,
    title,
    message,
    confirm,
    error = null,
    busy = false,
    onconfirm,
    oncancel,
  }: {
    open: boolean
    title: string
    message: string
    confirm: string
    error?: string | null
    busy?: boolean
    onconfirm: () => void | Promise<void>
    oncancel: () => void
  } = $props()
  let pending = $state(false)
  async function go() {
    pending = true
    try {
      await onconfirm()
    } finally {
      pending = false
    }
  }
</script>

<Dialog {open} {title} onclose={oncancel}>
  <p class="msg" style="unicode-bidi: isolate">{message}</p>
  {#if error}<p class="err" role="alert">{error}</p>{/if}
  <div class="row">
    <Button variant="ghost" onclick={oncancel}>{t.common.cancel}</Button>
    <Button variant="danger" disabled={busy || pending} onclick={go}>{confirm}</Button>
  </div>
</Dialog>

<style>
  .msg {
    margin: 0 0 12px;
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
