<script lang="ts">
  import { onMount, untrack } from 'svelte'
  import { api, ApiError } from '../../lib/api/client'
  import type { components } from '../../lib/api/schema'
  import type { AdminRoot, AdminUser, UserInput } from '../../lib/api/types'
  import { displayName } from '../../lib/fname'
  import { formatBytes, formatDate } from '../../lib/format'
  import { problemText } from '../../lib/errors'
  import { t } from '../../lib/i18n/en'
  import { navigate, router, setQuery } from '../../lib/router.svelte'
  import { session } from '../../lib/session.svelte'
  import Confirm from '../../components/ui/Confirm.svelte'
  import Button from '../../components/ui/Button.svelte'
  import Dialog from '../../components/ui/Dialog.svelte'
  import EmptyState from '../../components/ui/EmptyState.svelte'
  import Field from '../../components/ui/Field.svelte'
  import GrowthFigure from '../../components/ui/GrowthFigure.svelte'
  import RootSelect from '../../components/ui/RootSelect.svelte'
  import UserName from '../../components/ui/UserName.svelte'
  import QuotaInput from './QuotaInput.svelte'

  type Rank = components['schemas']['RankingRow']
  const sorts = ['user', 'role', 'quota_pct', 'usage', 'quota', 'growth', 'hardlinks', 'login'] as const
  type SortKey = (typeof sorts)[number]
  const textSorts: SortKey[] = ['user', 'role', 'login']
  const windows = ['7d', '30d', '90d'] as const
  type Role = 'user' | 'lister' | 'admin' | 'disabled'
  const roles: Role[] = ['user', 'lister', 'admin', 'disabled']

  const sort = $derived<SortKey>(sorts.find((s) => s === router.query.sort) ?? 'usage')
  const defaultDir = (k: SortKey) => (textSorts.includes(k) ? 'asc' : 'desc')
  const dir = $derived<'asc' | 'desc'>(router.query.dir === 'asc' || router.query.dir === 'desc' ? router.query.dir : defaultDir(sort))
  // The URL wins for a shared link; otherwise the account's window, shared with the Mounts view.
  const win = $derived(windows.find((w) => w === router.query.window) ?? windows.find((w) => w === session.prefs().admin_window) ?? '30d')
  const rootId = $derived(/^[0-9]+$/.test(router.query.root ?? '') ? Number(router.query.root) : null)

  // Accounts: every page, so each ranking row finds its account.
  let users = $state<AdminUser[] | null>(null)
  let usersFailed = $state(false)
  async function loadUsers() {
    usersFailed = false
    try {
      const all: AdminUser[] = []
      let cursor: string | undefined
      do {
        const p = await api.get<{ items: AdminUser[]; next_cursor: string | null }>('/admin/users', { cursor })
        all.push(...p.items)
        cursor = p.next_cursor ?? undefined
      } while (cursor)
      users = all
    } catch {
      usersFailed = true
    }
  }
  onMount(() => void loadUsers())

  // Usage figures: the ranking lists every user, in the chosen order.
  let ranking = $state<Rank[] | null>(null)
  let rankError = $state<string | null>(null)
  let roots = $state<AdminRoot[]>([])
  let seq = 0
  async function loadRanking(w: string, r: number | null) {
    const mine = ++seq
    rankError = null
    try {
      const p = await api.get<{ items: Rank[] }>('/admin/ranking', { sort: 'usage', window: w, root: r ?? undefined })
      if (mine === seq) ranking = p.items
    } catch (e) {
      if (mine !== seq) return
      rankError = e instanceof ApiError && e.status === 404 ? t.admin.ranking.unknownRoot : t.admin.ranking.failed
    }
  }
  $effect(() => {
    const [w, r] = [win, rootId]
    untrack(() => void loadRanking(w, r))
  })
  onMount(() => {
    api.get<{ items: AdminRoot[] }>('/admin/roots').then((p) => (roots = p.items), () => {})
  })

  const roleOf = (u: AdminUser): Role => (u.disabled ? 'disabled' : u.admin_local || u.admin_oidc ? 'admin' : u.can_list_files ? 'lister' : 'user')

  // One row per account, joined with its ranking figures (none for an account created since).
  const joined = $derived.by(() => {
    if (users === null || ranking === null) return null
    const byUser = new Map(ranking.map((r) => [r.user_id, r]))
    return users.map((u) => ({ u, r: byUser.get(u.id) ?? null }))
  })

  type Row = { u: AdminUser; r: Rank | null }
  // A missing figure sorts last in either direction.
  const value = (k: SortKey, { u, r }: Row): string | number | null => {
    switch (k) {
      case 'user': return u.username.toLowerCase()
      case 'role': return roles.indexOf(roleOf(u))
      case 'quota_pct': return r?.quota_pct ?? null
      case 'usage': return r?.bytes_alloc ?? null
      case 'quota': return r?.quota_bytes ?? u.quota_bytes
      case 'growth': return r?.growth_bytes ?? null
      case 'hardlinks': return r?.shared_hardlink_bytes ?? null
      case 'login': return u.last_login_at
    }
  }
  const rows = $derived.by(() => {
    if (joined === null) return null
    const sign = dir === 'asc' ? 1 : -1
    return [...joined].sort((a, b) => {
      const x = value(sort, a)
      const y = value(sort, b)
      if (x === null || y === null) return x === y ? 0 : x === null ? 1 : -1
      return (x < y ? -1 : x > y ? 1 : 0) * sign || a.u.username.localeCompare(b.u.username)
    })
  })

  const set = (patch: Record<string, string | undefined>) => setQuery(patch, { replace: true })
  function pickWindow(w: (typeof windows)[number]) {
    session.setPrefs({ admin_window: w })
    set({ window: undefined })
  }
  const sortBy = (k: SortKey) =>
    set(k === sort ? { dir: dir === 'asc' ? 'desc' : 'asc' } : { sort: k === 'usage' ? undefined : k, dir: undefined })
  const retry = () => {
    if (usersFailed) void loadUsers()
    if (rankError) void loadRanking(win, rootId)
  }

  // New user
  let creating = $state(false)
  let form = $state({ username: '', display_name: '', email: '', password: '', admin_local: false, can_list_files: false })
  let quotaValue = $state<number | null>(null)
  let quotaInvalid = $state(false)
  let formError = $state<string | null>(null)

  async function create(e: Event) {
    e.preventDefault()
    if (quotaInvalid) return
    formError = null
    const body: UserInput = {
      username: form.username,
      display_name: form.display_name,
      email: form.email === '' ? null : form.email,
      password: form.password,
      admin_local: form.admin_local,
      can_list_files: form.can_list_files,
      quota_bytes: quotaValue,
    }
    try {
      const u = await api.post<AdminUser>('/admin/users', body)
      creating = false
      navigate(`/admin/users/${u.id}`)
    } catch (err) {
      formError = problemText(err, t.admin.failed)
    }
  }

  let disabling = $state<AdminUser | null>(null)

  // Disabling keeps the other flags, so enabling again restores the earlier role.
  const rolePatch: Record<Role, Record<string, boolean>> = {
    user: { disabled: false, admin_local: false, can_list_files: false },
    lister: { disabled: false, admin_local: false, can_list_files: true },
    admin: { disabled: false, admin_local: true },
    disabled: { disabled: true },
  }

  async function setRole(u: AdminUser, role: Role): Promise<boolean> {
    formError = null
    try {
      const n = await api.patch<AdminUser>(`/admin/users/${u.id}`, rolePatch[role])
      users = users?.map((x) => (x.id === n.id ? n : x)) ?? null
      return true
    } catch (err) {
      formError = problemText(err, t.admin.failed)
      return false
    }
  }

  async function pickRole(e: Event & { currentTarget: HTMLSelectElement }, u: AdminUser) {
    const select = e.currentTarget
    const role = select.value as Role
    if (role === 'disabled') {
      select.value = roleOf(u)
      disabling = u
    } else if (!(await setRole(u, role))) {
      // The row still holds the old role; a native select would keep showing the refused one.
      select.value = roleOf(u)
    }
  }
</script>

<section>
  <header class="top">
    <h1>{t.admin.sections.users}</h1>
    <RootSelect label={t.admin.ranking.root} {roots} value={rootId} onchange={(id) => set({ root: id === null ? undefined : String(id) })} />
    <div class="win" role="group" aria-label={t.usage.window}>
      {#each windows as w (w)}
        <button class:on={win === w} aria-pressed={win === w} onclick={() => pickWindow(w)}>{t.history.windows[w]}</button>
      {/each}
    </div>
    <Button onclick={() => ((creating = true), (formError = null))}>{t.admin.users.new}</Button>
  </header>

  {#if formError && !creating}<p class="err" role="alert">{formError}</p>{/if}
  {#if usersFailed || rankError}
    <EmptyState title={t.common.error} body={rankError ?? t.admin.users.loadFailed}>
      <Button onclick={retry}>{t.common.retry}</Button>
    </EmptyState>
  {:else if rows === null}
    <p>{t.common.loading}</p>
  {:else if rows.length === 0}
    <EmptyState title={t.admin.sections.users} body={t.admin.users.empty} />
  {:else}
    <div class="scroll">
      <table>
        <colgroup>
          <col class="user" />
          <col class="role-col" />
          <col class="quota-used" />
          <col class="usage" />
          <col class="quota" />
          <col class="growth" />
          <col class="half" />
          <col class="half" />
          <col class="half" />
          <col class="act" />
        </colgroup>
        <thead>
          <tr>
            {#each [
              ['user', t.admin.ranking.user, false],
              ['role', t.admin.users.role, false],
              ['quota_pct', t.admin.ranking.quotaPct, false],
              ['usage', t.admin.ranking.allocated, true],
              ['quota', t.admin.ranking.quota, true],
              ['growth', t.admin.ranking.growth, true],
              ['hardlinks', t.admin.ranking.shared, true],
              ['login', t.admin.users.lastLogin, false],
            ] as [SortKey, string, boolean][] as [k, label, right] (k)}
              {#if k === 'login'}<th class="gap"></th>{/if}
              <th class:r={right} aria-sort={sort === k ? (dir === 'asc' ? 'ascending' : 'descending') : undefined}>
                <button class:on={sort === k} aria-label={t.admin.ranking.sortBy(label)} onclick={() => sortBy(k)}>{label}{sort === k ? (dir === 'asc' ? ' ↑' : ' ↓') : ''}</button>
              </th>
            {/each}
            <th></th>
          </tr>
        </thead>
        <tbody>
          {#each rows as { u, r } (u.id)}
            <tr class:off={u.disabled}>
              <td><UserName id={u.id} name={u.username} /></td>
              <td>
                <select
                  class={['role', roleOf(u)]}
                  aria-label={t.admin.users.roleOf(displayName(u.username))}
                  title={u.admin_oidc && !u.admin_local ? t.admin.users.roleManaged : undefined}
                  value={roleOf(u)}
                  disabled={session.me?.id === u.id || (u.admin_oidc && !u.admin_local)}
                  onchange={(e) => pickRole(e, u)}
                >
                  {#each roles as role (role)}<option value={role}>{t.admin.users.roles[role]}</option>{/each}
                </select>
              </td>
              <td class="nowrap">
                {#if r === null || r.quota_pct === null || r.quota_bytes === null}
                  {t.common.none}
                {:else}
                  <span class="meter" role="progressbar" aria-label={t.admin.ranking.quotaPct} aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.min(100, Math.round(r.quota_pct))}>
                    <span class={['fill', r.quota_pct >= 95 ? 'hi' : r.quota_pct >= 80 ? 'mid' : '']} style:width={`${Math.min(100, r.quota_pct)}%`}></span>
                    <i style="left: 80%"></i><i style="left: 95%"></i>
                  </span>
                  <span class="mono">{Math.round(r.quota_pct)} %</span>
                {/if}
              </td>
              <td class="mono r">{r ? formatBytes(r.bytes_alloc) : t.common.none}</td>
              <td class="mono r">{r?.quota_bytes != null ? formatBytes(r.quota_bytes) : t.common.none}</td>
              <td class="r"><GrowthFigure bytes={r ? r.growth_bytes : null} /></td>
              <td class="mono r">{r ? formatBytes(r.shared_hardlink_bytes) : t.common.none}</td>
              <td class="gap"></td>
              <td class="mono nowrap">{u.last_login_at === null ? t.admin.users.never : formatDate(u.last_login_at)}</td>
              <td class="act"><Button variant="ghost" onclick={() => navigate(`/admin/users/${u.id}`)}>{t.admin.users.edit}</Button></td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</section>

<Confirm
  open={disabling !== null}
  title={t.admin.users.disable}
  message={t.admin.users.disableBody(disabling ? displayName(disabling.username) : "")}
  confirm={t.admin.users.disable}
  onconfirm={async () => {
    const u = disabling
    disabling = null
    if (u) await setRole(u, 'disabled')
  }}
  oncancel={() => (disabling = null)}
/>

<Dialog open={creating} title={t.admin.users.new} onclose={() => (creating = false)}>
  <form onsubmit={create} novalidate>
    <Field label={t.admin.users.username}><input bind:value={form.username} required autocomplete="off" /></Field>
    <Field label={t.admin.users.displayName}><input bind:value={form.display_name} autocomplete="off" /></Field>
    <Field label={t.admin.users.email}><input type="email" bind:value={form.email} autocomplete="off" /></Field>
    <Field label={t.admin.users.password}><input type="password" bind:value={form.password} required autocomplete="new-password" /></Field>
    <QuotaInput bind:value={quotaValue} bind:invalid={quotaInvalid} />
    <label class="chk"><input type="checkbox" bind:checked={form.admin_local} /> {t.admin.users.adminLocal}</label>
    <label class="chk"><input type="checkbox" bind:checked={form.can_list_files} /> {t.admin.users.canList}</label>
    {#if formError}<p class="err" role="alert">{formError}</p>{/if}
    <div class="row">
      <Button variant="ghost" type="button" onclick={() => (creating = false)}>{t.common.cancel}</Button>
      <Button type="submit" disabled={quotaInvalid}>{t.admin.users.create}</Button>
    </div>
  </form>
</Dialog>

<style>
  .top {
    display: flex;
    gap: 16px;
    align-items: center;
    margin-bottom: 12px;
    flex-wrap: wrap;
  }
  h1 {
    font: 600 24px var(--serif);
    margin: 0;
    flex: 1;
  }
  .win {
    display: flex;
    border: var(--edge);
    border-radius: var(--r-pill);
    overflow: hidden;
  }
  .win button {
    border: none;
    background: var(--card);
    padding: 3px 12px;
    cursor: pointer;
  }
  .win button.on {
    background: var(--ink);
    color: var(--paper);
  }
  .scroll {
    overflow-x: auto;
  }
  table {
    width: 100%;
    border-collapse: collapse;
    table-layout: fixed;
  }
  /* Quota used, usage and quota are sized to their content so they sit close together; the other columns share the rest equally. */
  col.even {
    width: 12%;
  }
  /* Role and growth share one width. */
  col.role-col,
  col.growth {
    width: 16%;
  }
  /* Two thirds of the role column. */
  col.user {
    width: 10.67%;
  }
  /* Hardlinks, the gap before Last sign-in and Last sign-in are half the growth column. */
  col.half {
    width: 8%;
  }
  /* The Edit column is only as wide as its button and padding. */
  col.act {
    width: 72px;
  }
  col.quota-used {
    width: 150px;
  }
  col.usage,
  col.quota {
    width: 117px;
  }
  th:nth-child(3),
  td:nth-child(3),
  th:nth-child(4),
  td:nth-child(4),
  th:nth-child(5),
  td:nth-child(5) {
    white-space: nowrap;
  }
  th,
  td {
    text-align: left;
    padding: 6px 8px;
    border-bottom: 1px solid var(--hair);
    vertical-align: middle;
  }
  th {
    color: var(--ink-2);
    font-weight: 500;
    font-size: 12px;
  }
  .r {
    text-align: right;
  }
  .nowrap {
    white-space: nowrap;
  }
  th button {
    border: none;
    background: none;
    cursor: pointer;
    color: inherit;
    font: inherit;
  }
  th button.on {
    color: var(--ink);
    text-decoration: underline;
  }
  .sub {
    display: block;
    color: var(--ink-2);
    font-size: 12px;
  }
  tr.off td {
    color: var(--ink-2);
    background-image: repeating-linear-gradient(
      -45deg,
      transparent 0 6px,
      color-mix(in srgb, var(--ink-2) 14%, transparent) 6px 7px
    );
  }
  .role {
    border: var(--edge);
    border-radius: var(--r-pill);
    padding: 1px 6px;
    font: 500 12px var(--sans, inherit);
    color: var(--ink);
    background: var(--card);
    cursor: pointer;
  }
  .role:disabled {
    cursor: default;
    opacity: 0.8;
  }
  .role.lister {
    background: color-mix(in srgb, var(--sky) 22%, var(--card));
  }
  .role.admin {
    background: var(--terra-bg);
  }
  .role.disabled {
    background: color-mix(in srgb, var(--ink-2) 18%, var(--card));
    color: var(--ink-2);
  }
  .meter {
    position: relative;
    display: inline-block;
    width: 80px;
    height: 10px;
    border: var(--edge);
    border-radius: var(--r-pill);
    background: var(--card);
    overflow: hidden;
    vertical-align: middle;
    margin-right: 6px;
  }
  .fill {
    display: block;
    height: 100%;
    background: var(--sage);
  }
  .fill.mid {
    background: var(--mustard);
  }
  .fill.hi {
    background: var(--danger);
  }
  .meter i {
    position: absolute;
    top: 0;
    bottom: 0;
    width: 1px;
    background: var(--ink);
  }
  .err {
    color: var(--danger);
  }
  .chk {
    display: block;
    margin-bottom: 8px;
  }
  .row {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
  }
</style>
