<script lang="ts">
  import Button from '../components/ui/Button.svelte'
  import Card from '../components/ui/Card.svelte'
  import Field from '../components/ui/Field.svelte'
  import UserName from '../components/ui/UserName.svelte'
  import { api, ApiError } from '../lib/api/client'
  import { displayName } from '../lib/fname'
  import { t } from '../lib/i18n/en'
  import { router } from '../lib/router.svelte'
  import { session } from '../lib/session.svelte'

  $effect(() => {
    if (!session.authConfig) session.loadAuthConfig().catch(() => {})
  })

  let current = $state('')
  let next = $state('')
  let pwError = $state<string | null>(null)
  let pwDone = $state(false)
  let linkError = $state<string | null>(null)

  const me = $derived(session.me)
  const banner = $derived(
    router.query.oidc === 'linked'
      ? t.profile.ssoLinked
      : router.query.oidc_error
        ? Object.hasOwn(t.oidcError, router.query.oidc_error)
          ? t.oidcError[router.query.oidc_error]
          : t.oidcError.unknown
        : null,
  )

  async function changePassword(e: SubmitEvent) {
    e.preventDefault()
    pwError = null
    pwDone = false
    try {
      await api.post('/me/password', { current, new: next })
      current = next = ''
      pwDone = true
    } catch (err) {
      pwError = err instanceof ApiError ? err.detail || err.title : t.common.error
    }
  }

  async function link() {
    linkError = null
    try {
      const r = await api.post<{ url: string }>('/me/link-oidc')
      location.assign(r.url)
    } catch (err) {
      linkError = err instanceof ApiError ? err.detail || err.title : t.common.error
    }
  }
</script>

<main class="page">
  <h1>{t.profile.title}</h1>
  {#if banner}<p class="banner" role="status">{banner}</p>{/if}
  {#if me}
    <Card title={t.profile.account}>
      <p><strong><UserName id={me.id} name={me.display_name || me.username} /></strong> <span class="mono">{displayName(me.username)}</span></p>
      {#if me.email}<p>{displayName(me.email)}</p>{/if}
    </Card>
    {#if me.has_password}
      <Card title={t.profile.password}>
        <form onsubmit={changePassword}>
          <Field label={t.profile.current}>
            <input type="password" bind:value={current} autocomplete="current-password" required />
          </Field>
          <Field label={t.profile.next} error={pwError}>
            <input type="password" bind:value={next} autocomplete="new-password" required />
          </Field>
          {#if pwDone}<p role="status">{t.profile.changed}</p>{/if}
          <Button type="submit">{t.profile.password}</Button>
        </form>
      </Card>
    {/if}
    {#if session.authConfig?.oidc || me.oidc_linked}
      <Card title={t.profile.sso}>
        <p>{me.oidc_linked ? t.profile.linked : t.profile.notLinked}</p>
        {#if !me.oidc_linked}
          {#if linkError}<p role="alert">{linkError}</p>{/if}
          <Button onclick={link}>{t.profile.link}</Button>
        {/if}
      </Card>
    {/if}
  {/if}
</main>

<style>
  .page {
    max-width: 560px;
    margin: 24px auto;
    padding: 0 16px;
    display: grid;
    gap: 16px;
  }
  .banner {
    background: var(--mustard-bg);
    border: var(--edge);
    border-radius: 6px;
    padding: 6px 10px;
  }
</style>
