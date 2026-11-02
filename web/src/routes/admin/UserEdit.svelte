<script lang="ts">
  import { onMount } from 'svelte'
  import { api, ApiError } from '../../lib/api/client'
  import type { AdminUser, UserPatch } from '../../lib/api/types'
  import { displayName } from '../../lib/fname'
  import type { components } from '../../lib/api/schema'
  import { problemText } from '../../lib/errors'
  import { t } from '../../lib/i18n/en'
  import { diffPatch } from '../../lib/patch'
  import { navigate } from '../../lib/router.svelte'
  import { session } from '../../lib/session.svelte'
  import { toast } from '../../lib/toast.svelte'
  import Button from '../../components/ui/Button.svelte'
  import Card from '../../components/ui/Card.svelte'
  import Confirm from '../../components/ui/Confirm.svelte'
  import EmptyState from '../../components/ui/EmptyState.svelte'
  import Field from '../../components/ui/Field.svelte'
  import UserName from '../../components/ui/UserName.svelte'
  import QuotaInput from './QuotaInput.svelte'
  import UserPaths from './UserPaths.svelte'

  let { id }: { id: number } = $props()

  type Form = {
    display_name: string
    email: string | null
    admin_local: boolean
    can_list_files: boolean
    quota_bytes: number | null
    disabled: boolean
    password: string
  }
  const keys: (keyof Form)[] = ['display_name', 'email', 'admin_local', 'can_list_files', 'quota_bytes', 'disabled', 'password']

  let user = $state<AdminUser | null>(null)
  let before = $state<Form | null>(null)
  let form = $state<Form>({ display_name: '', email: null, admin_local: false, can_list_files: false, quota_bytes: null, disabled: false, password: '' })
  let quotaInvalid = $state(false)
  let usage = $state<number | null>(null)
  let failed = $state(false)
  let error = $state<string | null>(null)
  let saving = $state(false)

  function adopt(u: AdminUser) {
    user = u
    const f: Form = { display_name: u.display_name, email: u.email, admin_local: u.admin_local, can_list_files: u.can_list_files, quota_bytes: u.quota_bytes, disabled: u.disabled, password: '' }
    before = { ...f }
    form = { ...f }
  }

  async function load() {
    failed = false
    try {
      adopt(await api.get<AdminUser>(`/admin/users/${id}`))
    } catch {
      failed = true
    }
  }
  onMount(() => void load())

  // Re-reads the account without touching the form, so unsaved edits survive link, unlink and revoke.
  async function refreshUser() {
    user = await api.get<AdminUser>(`/admin/users/${id}`)
  }

  // Without usage figures the quota can still be edited, so a failure only hides the meter.
  onMount(() => {
    api.get<{ items: components['schemas']['RankingRow'][] }>('/admin/ranking', { sort: 'usage' }).then(
      (p) => (usage = p.items.find((r) => r.user_id === id)?.bytes_alloc ?? null),
      () => {},
    )
  })

  async function save(e: Event) {
    e.preventDefault()
    if (!before || quotaInvalid) return
    error = null
    const after: Form = { ...form, email: form.email === '' ? null : form.email }
    const body = diffPatch(before, after, keys) as UserPatch
    if (Object.keys(body).length === 0) return
    saving = true
    try {
      adopt(await api.patch<AdminUser>(`/admin/users/${id}`, body))
      toast(t.admin.saved, 'saved')
    } catch (err) {
      // The form keeps what was typed so it can be corrected.
      error = problemText(err, t.admin.failed)
    } finally {
      saving = false
    }
  }

  // Destructive and session actions, each through a confirm that names the user.
  type Act = 'revoke' | 'delete' | 'unlink' | null
  let act = $state<Act>(null)
  let actError = $state<string | null>(null)
  let subject = $state('')
  let linkError = $state<string | null>(null)

  async function run() {
    actError = null
    try {
      if (act === 'revoke') await api.post(`/admin/users/${id}/revoke-sessions`)
      else if (act === 'unlink') await api.post(`/admin/users/${id}/unlink`)
      else if (act === 'delete') {
        await api.del(`/admin/users/${id}`)
        act = null
        navigate('/admin/users')
        return
      }
      const done = act
      act = null
      // The action succeeded and its dialog is closed, so a failed refresh is reported on its
      // own instead of through the dialog's error line.
      if (done !== 'revoke') await refreshUser().catch(() => toast(t.admin.users.refreshFailed, 'error'))
    } catch (err) {
      actError = problemText(err, t.admin.failed)
    }
  }

  async function link(e: Event) {
    e.preventDefault()
    linkError = null
    try {
      await api.post(`/admin/users/${id}/link`, { subject })
      subject = ''
      await refreshUser()
    } catch (err) {
      linkError = problemText(err, t.admin.failed)
    }
  }

  const name = $derived(user ? displayName(user.username) : '')
  // The role follows the single sign-on group when it grants admin, and nobody changes their own role.
  const roleManaged = $derived(user?.admin_oidc === true && !user.admin_local)
  const roleLocked = $derived(session.me?.id === user?.id || roleManaged)
  const confirmCfg = $derived(
    act === 'revoke'
      ? { title: t.admin.users.revoke, message: t.admin.users.revokeBody(name), confirm: t.admin.users.revoke }
      : act === 'delete'
        ? { title: t.admin.users.remove, message: t.admin.users.removeBody(name), confirm: t.admin.users.remove }
        : { title: t.admin.users.unlink, message: t.admin.users.unlinkBody(name), confirm: t.admin.users.unlink },
  )
</script>

<section>
  <div class="top full">
    <a href="/admin/users">← {t.admin.users.back}</a>
    {#if user}
      <div class="danger">
        <Button variant="ghost" onclick={() => ((act = 'revoke'), (actError = null))}>{t.admin.users.revoke}</Button>
        {#if session.me?.id !== user.id}
          <Button variant="danger" onclick={() => ((act = 'delete'), (actError = null))}>{t.admin.users.remove}</Button>
        {/if}
      </div>
    {/if}
  </div>
  {#if failed}
    <EmptyState title={t.common.error} body={t.admin.users.loadFailed}>
      <Button onclick={load}>{t.common.retry}</Button>
    </EmptyState>
  {:else if !user}
    <p>{t.common.loading}</p>
  {:else}
    <div class="main" class:off={form.disabled}>
      <Card>
        {#snippet head()}{#if user}<UserName id={user.id} name={user.username} />{/if}{/snippet}
        {#snippet actions()}
          <button
            type="button"
            class={['toggle', form.disabled && 'off']}
            aria-pressed={form.disabled}
            disabled={session.me?.id === user?.id}
            onclick={() => (form.disabled = !form.disabled)}
          >{form.disabled ? t.admin.users.disabled : t.admin.users.enabled}</button>
          <select
            class={['role', form.admin_local ? 'admin' : 'user']}
            aria-label={t.admin.users.roleOf(name)}
            title={roleManaged ? t.admin.users.roleManaged : undefined}
            value={form.admin_local ? 'admin' : 'user'}
            disabled={roleLocked}
            onchange={(e) => (form.admin_local = e.currentTarget.value === 'admin')}
          >
            <option value="user">{t.admin.users.roles.user}</option>
            <option value="admin">{t.admin.users.roles.admin}</option>
          </select>
        {/snippet}
        <form onsubmit={save} novalidate>
          <Field label={t.admin.users.displayName}><input bind:value={form.display_name} autocomplete="off" /></Field>
          <Field label={t.admin.users.email}>
            <input type="email" value={form.email ?? ''} oninput={(e) => (form.email = e.currentTarget.value)} autocomplete="off" />
          </Field>
          {#key user.quota_bytes}
            <QuotaInput bind:value={form.quota_bytes} bind:invalid={quotaInvalid} {usage} />
          {/key}
          <Field label={t.admin.users.newPassword}><input type="password" bind:value={form.password} autocomplete="new-password" /></Field>
          {#if user.admin_oidc}<p class="cur">{t.admin.users.adminOidc}</p>{/if}
          <label class="chk"><input type="checkbox" bind:checked={form.can_list_files} /> {t.admin.users.canList}</label>
          {#if error}<p class="err" role="alert">{error}</p>{/if}
          <Button type="submit" disabled={saving || quotaInvalid}>{t.common.save}</Button>
        </form>
      </Card>

      <Card title={t.admin.users.linked}>
        {#if user.oidc_linked}
          <Button variant="ghost" onclick={() => ((act = 'unlink'), (actError = null))}>{t.admin.users.unlink}</Button>
        {:else}
          <form onsubmit={link} class="inline">
            <Field label={t.admin.users.subject} error={linkError}><input bind:value={subject} required autocomplete="off" /></Field>
            <Button type="submit" disabled={subject.trim() === ''}>{t.admin.users.link}</Button>
          </form>
        {/if}
      </Card>
    </div>

    <div class="side" class:off={form.disabled}>
      <UserPaths userId={id} />
    </div>

  {/if}
</section>

<Confirm open={act !== null} {...confirmCfg} error={actError} onconfirm={run} oncancel={() => (act = null)} />

<style>
  section {
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    gap: 16px;
    max-width: 1200px;
  }
  .main > :global(.card:first-child h2) {
    font-size: 22px;
  }
  .chk {
    display: block;
    margin-bottom: 8px;
  }
  .err {
    color: var(--danger);
  }
  /* Same look as the role column in the users table, at the height of the card's header line. */
  /* Disabled: the toggle turns red and every card is striped, as a disabled row is in the users table. */
  .toggle {
    height: 34px;
    border: var(--edge);
    border-radius: var(--r-pill);
    padding: 0 14px;
    font: 500 12px var(--sans, inherit);
    color: var(--ink);
    background: var(--card);
    cursor: pointer;
  }
  .toggle.off {
    background: var(--danger);
    border-color: var(--danger);
    color: var(--paper);
  }
  .off :global(.card) {
    background-image: repeating-linear-gradient(-45deg, transparent 0 6px, color-mix(in srgb, var(--ink-2) 14%, transparent) 6px 7px);
  }
  .role {
    height: 34px;
    border: var(--edge);
    border-radius: var(--r-pill);
    padding: 0 14px;
    font: 500 12px var(--sans, inherit);
    color: var(--ink);
    background: var(--card);
    cursor: pointer;
  }
  .role:disabled {
    cursor: default;
    opacity: 0.8;
  }
  .role.admin {
    background: var(--terra-bg);
  }
  .cur {
    margin: 0 0 8px;
    color: var(--ink-2);
    font-size: 13px;
  }
  .top {
    display: flex;
    align-items: center;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 8px;
  }
  .danger {
    display: flex;
    gap: 8px;
  }
  .main,
  .side {
    display: grid;
    gap: 16px;
    align-content: start;
    min-width: 0;
  }
  .full {
    grid-column: 1 / -1;
  }
  @media (min-width: 900px) {
    section {
      grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
    }
  }
</style>
