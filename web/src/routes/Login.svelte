<script lang="ts">
  import { onMount } from 'svelte'
  import Button from '../components/ui/Button.svelte'
  import Card from '../components/ui/Card.svelte'
  import Field from '../components/ui/Field.svelte'
  import { api, ApiError } from '../lib/api/client'
  import { t } from '../lib/i18n/en'
  import { hardNavigate, router, safeNext } from '../lib/router.svelte'
  import { session } from '../lib/session.svelte'

  let username = $state('')
  let password = $state('')
  let error = $state<string | null>(null)
  let busy = $state(false)
  let setupNeeded = $state(false)

  const oidcCode = $derived(router.query.oidc_error)
  const oidcMessage = $derived(oidcCode ? (Object.hasOwn(t.oidcError, oidcCode) ? t.oidcError[oidcCode] : t.oidcError.unknown) : null)

  onMount(() => {
    session.loadAuthConfig().catch(() => {})
    api
      .get<{ needed: boolean }>('/setup/status')
      .then((s) => (setupNeeded = s.needed))
      .catch(() => {})
  })

  async function submit(e: SubmitEvent) {
    e.preventDefault()
    busy = true
    error = null
    try {
      await api.post('/auth/login', { username, password })
      hardNavigate(safeNext(router.query.next ?? null))
    } catch (err) {
      if (err instanceof ApiError && err.status === 429) error = t.login.tooMany(err.retryAfter ?? 60)
      else if (err instanceof ApiError && err.status === 401) error = t.login.invalid
      else error = t.login.failed
    } finally {
      busy = false
    }
  }
</script>

<main class="page">
  <div class="stack">
    <h1 class="brand">Hogwatch</h1>
    <Card title={t.login.title}>
      {#if oidcMessage}<p class="msg" role="alert">{oidcMessage}</p>{/if}
      {#if setupNeeded}<p class="msg">{t.login.setupHint}</p>{/if}
      {#if session.authConfig?.local}
        <form onsubmit={submit}>
          <Field label={t.login.username}>
            <input bind:value={username} autocomplete="username" required />
          </Field>
          <Field label={t.login.password}>
            <input type="password" bind:value={password} autocomplete="current-password" required />
          </Field>
          {#if error}<p class="msg" role="alert">{error}</p>{/if}
          <Button type="submit" disabled={busy}>{t.login.submit}</Button>
        </form>
      {/if}
      {#if session.authConfig?.oidc}
        <p><a class="sso" href="/api/v1/auth/oidc/start">{t.login.sso}</a></p>
      {/if}
    </Card>
  </div>
</main>

<style>
  .page {
    min-height: 100vh;
    min-height: 100dvh;
    background-image: repeating-linear-gradient(
      -45deg,
      rgb(31 27 22 / 0.045) 0 1px,
      transparent 1px 9px
    );
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 0 16px;
  }
  .stack {
    width: 100%;
    max-width: 380px;
  }
  .brand {
    font-size: 48px;
    font-weight: 700;
    text-align: center;
    line-height: 1.1;
    margin: 0 0 24px;
  }
  .msg {
    background: var(--danger-bg);
    border: var(--edge);
    border-radius: 6px;
    padding: 6px 10px;
  }
  .sso {
    font-weight: 500;
  }
</style>
