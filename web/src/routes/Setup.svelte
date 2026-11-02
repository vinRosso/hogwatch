<script lang="ts">
  import Button from '../components/ui/Button.svelte'
  import Card from '../components/ui/Card.svelte'
  import Field from '../components/ui/Field.svelte'
  import { onMount } from 'svelte'
  import { api, ApiError } from '../lib/api/client'
  import { t } from '../lib/i18n/en'
  import { hardNavigate } from '../lib/router.svelte'

  let username = $state('')
  let displayName = $state('')
  let email = $state('')
  let password = $state('')
  let error = $state<string | null>(null)
  let done = $state(false)
  let busy = $state(false)

  // Setup is open only while no admin exists; a later visit says so instead of showing a form.
  onMount(() => {
    api
      .get<{ needed: boolean }>('/setup/status')
      .then((s) => {
        if (s.needed === false) done = true
      })
      .catch(() => {})
  })

  async function submit(e: SubmitEvent) {
    e.preventDefault()
    busy = true
    error = null
    try {
      await api.post('/setup', { username, display_name: displayName, email: email || null, password })
      await api.post('/auth/login', { username, password })
      hardNavigate('/')
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) done = true
      else if (err instanceof ApiError && err.detail) error = err.detail
      else error = t.setup.failed
    } finally {
      busy = false
    }
  }
</script>

<main class="page">
  <Card title={t.setup.title}>
    {#if done}
      <p>{t.setup.done}</p>
      <a href="/login">{t.setup.goLogin}</a>
    {:else}
      <form onsubmit={submit}>
        <Field label={t.setup.username}><input bind:value={username} autocomplete="username" required /></Field>
        <Field label={t.setup.displayName}><input bind:value={displayName} /></Field>
        <Field label={t.setup.email}><input type="email" bind:value={email} autocomplete="email" /></Field>
        <Field label={t.setup.password}>
          <input type="password" bind:value={password} autocomplete="new-password" required />
        </Field>
        {#if error}<p role="alert">{error}</p>{/if}
        <Button type="submit" disabled={busy}>{t.setup.submit}</Button>
      </form>
    {/if}
  </Card>
</main>

<style>
  .page {
    max-width: 420px;
    margin: 10vh auto;
    padding: 0 16px;
  }
</style>
