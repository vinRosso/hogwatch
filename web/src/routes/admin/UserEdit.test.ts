import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import { beforeEach, expect, it, vi } from 'vitest'

vi.mock('../../lib/api/client', async (orig) => {
  const m = await orig<typeof import('../../lib/api/client')>()
  return { ...m, api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() } }
})

import { api, ApiError } from '../../lib/api/client'
import { t } from '../../lib/i18n/en'
import { toasts } from '../../lib/toast.svelte'
import UserEdit from './UserEdit.svelte'

const user = {
  id: 5, username: 'bob', display_name: 'Bob', email: 'b@x.org', admin_local: false, admin_oidc: false, can_list_files: false,
  quota_bytes: 5, quota_basis: 'allocated', disabled: false, oidc_linked: false, has_password: true, created_at: 1, last_login_at: null,
}

beforeEach(() => {
  for (const f of [api.get, api.post, api.patch, api.del]) vi.mocked(f).mockReset()
  vi.mocked(api.get).mockImplementation(async (p: string) => (p.endsWith('/paths') || p === '/admin/ranking' ? { items: [] } : p === '/admin/roots' ? { items: [] } : user) as never)
})

const amount = () => screen.getByLabelText('Quota amount') as HTMLInputElement
const unit = () => screen.getByLabelText('Quota unit') as HTMLSelectElement

// A user with this quota who has used this many bytes.
function withUsage(quota: number | null, used: number) {
  vi.mocked(api.get).mockImplementation(async (p: string) =>
    (p === '/admin/ranking'
      ? { items: [{ user_id: 5, username: 'bob', bytes_alloc: used, shared_hardlink_bytes: 0, quota_bytes: quota, quota_pct: null, growth_bytes: null }] }
      : p.endsWith('/paths') || p === '/admin/roots'
        ? { items: [] }
        : { ...user, quota_bytes: quota }) as never,
  )
}

it('loads a saved quota as an amount and a unit', async () => {
  withUsage(10_000_000_000, 0)
  render(UserEdit, { id: 5 })
  await waitFor(() => expect(amount().value).toBe('10'))
  expect(unit().value).toBe('GB')
})

it('does not rewrite a quota that does not fit one unit when it was not edited', async () => {
  withUsage(1_234_567_890, 0)
  vi.mocked(api.patch).mockResolvedValue({ ...user, quota_bytes: 1_234_567_890, display_name: 'Robert' } as never)
  render(UserEdit, { id: 5 })
  await waitFor(() => expect(screen.getByLabelText('Display name')).toBeTruthy())
  await fireEvent.input(screen.getByLabelText('Display name'), { target: { value: 'Robert' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() => expect(api.patch).toHaveBeenCalled())
  expect(vi.mocked(api.patch).mock.calls[0][1]).toEqual({ display_name: 'Robert' })
})

it('saves the amount in the chosen unit', async () => {
  withUsage(10_000_000_000, 0)
  vi.mocked(api.patch).mockResolvedValue({ ...user, quota_bytes: 2_000_000_000_000 } as never)
  render(UserEdit, { id: 5 })
  await waitFor(() => expect(amount().value).toBe('10'))
  await fireEvent.input(amount(), { target: { value: '2' } })
  await fireEvent.change(unit(), { target: { value: 'TB' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() => expect(api.patch).toHaveBeenCalled())
  expect(vi.mocked(api.patch).mock.calls[0][1]).toEqual({ quota_bytes: 2_000_000_000_000 })
})

it('sets the administrator role from the role combobox', async () => {
  vi.mocked(api.patch).mockResolvedValue({ ...user, admin_local: true } as never)
  render(UserEdit, { id: 5 })
  const role = await waitFor(() => screen.getByRole('combobox', { name: 'Role of bob' }) as HTMLSelectElement)
  expect(role.value).toBe('user')
  await fireEvent.change(role, { target: { value: 'admin' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() => expect(api.patch).toHaveBeenCalled())
  expect(vi.mocked(api.patch).mock.calls[0][1]).toEqual({ admin_local: true })
})

it('disables the account from the Enabled toggle', async () => {
  vi.mocked(api.patch).mockResolvedValue({ ...user, disabled: true } as never)
  render(UserEdit, { id: 5 })
  const toggle = await waitFor(() => screen.getByRole('button', { name: 'Enabled' }))
  expect(toggle.getAttribute('aria-pressed')).toBe('false')
  await fireEvent.click(toggle)
  expect(toggle.getAttribute('aria-pressed')).toBe('true')
  expect(toggle.textContent).toBe('Disabled')
  await fireEvent.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() => expect(api.patch).toHaveBeenCalled())
  expect(vi.mocked(api.patch).mock.calls[0][1]).toEqual({ disabled: true })
})

it('refuses an amount that is not a number', async () => {
  render(UserEdit, { id: 5 })
  await waitFor(() => expect(amount()).toBeTruthy())
  await fireEvent.input(amount(), { target: { value: 'lots' } })
  expect(screen.getByRole('alert').textContent).toMatch(/Enter a number/)
  expect((screen.getByRole('button', { name: 'Save' }) as HTMLButtonElement).disabled).toBe(true)
})

it('shows usage against the quota being typed', async () => {
  withUsage(10_000_000_000, 6_200_000_000)
  render(UserEdit, { id: 5 })
  await waitFor(() => expect(screen.getByText(/Uses 6\.20 GB, 62 % of this quota\. Warnings at 8\.00 GB and 9\.50 GB\./)).toBeTruthy())
  await fireEvent.input(amount(), { target: { value: '20' } })
  expect(screen.getByText(/Uses 6\.20 GB, 31 % of this quota\. Warnings at 16\.0 GB and 19\.0 GB\./)).toBeTruthy()
  expect(screen.getByRole('progressbar').getAttribute('aria-valuenow')).toBe('31')
})

it('says there is no quota once the amount is empty', async () => {
  withUsage(10_000_000_000, 6_200_000_000)
  render(UserEdit, { id: 5 })
  await waitFor(() => expect(screen.getByRole('progressbar')).toBeTruthy())
  await fireEvent.input(amount(), { target: { value: '' } })
  expect(screen.getByText('No quota. Uses 6.20 GB.')).toBeTruthy()
  expect(screen.queryByRole('progressbar')).toBeNull()
})

it('sends only the changed keys', async () => {
  vi.mocked(api.patch).mockResolvedValue({ ...user, display_name: 'Robert' } as never)
  render(UserEdit, { id: 5 })
  await waitFor(() => expect(screen.getByLabelText('Display name')).toBeTruthy())
  await fireEvent.input(screen.getByLabelText('Display name'), { target: { value: 'Robert' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() => expect(api.patch).toHaveBeenCalled())
  expect(vi.mocked(api.patch).mock.calls[0]).toEqual(['/admin/users/5', { display_name: 'Robert' }])
})

it('clearing the quota sends an explicit null', async () => {
  vi.mocked(api.patch).mockResolvedValue({ ...user, quota_bytes: null } as never)
  render(UserEdit, { id: 5 })
  await waitFor(() => expect(amount()).toBeTruthy())
  await fireEvent.input(amount(), { target: { value: '' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() => expect(api.patch).toHaveBeenCalled())
  expect(vi.mocked(api.patch).mock.calls[0][1]).toEqual({ quota_bytes: null })
})

it('shows a 409 detail and keeps the typed values', async () => {
  vi.mocked(api.patch).mockRejectedValue(new ApiError(409, 'Conflict', 'cannot remove the last administrator', null))
  render(UserEdit, { id: 5 })
  await waitFor(() => expect(screen.getByLabelText('Display name')).toBeTruthy())
  await fireEvent.input(screen.getByLabelText('Display name'), { target: { value: 'Robert' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() => expect(screen.getByText('cannot remove the last administrator')).toBeTruthy())
  expect((screen.getByLabelText('Display name') as HTMLInputElement).value).toBe('Robert')
})

it('rejects a typed path with .. inline and does not post', async () => {
  vi.mocked(api.get).mockImplementation(async (p: string) =>
    (p.endsWith('/paths') || p === '/admin/ranking' ? { items: [] } : p === '/admin/roots' ? { items: [{ id: 1, name: 'tank' }] } : user) as never,
  )
  render(UserEdit, { id: 5 })
  await fireEvent.click(await screen.findByRole('button', { name: 'Add a folder of tank' }))
  await fireEvent.input(screen.getByLabelText(/^Folder/), { target: { value: 'a/../b' } })
  expect(screen.getByText(/\. or \.\. parts/)).toBeTruthy()
  await fireEvent.click(screen.getByRole('button', { name: 'Assign folder' }))
  expect(api.post).not.toHaveBeenCalled()
})

it('keeps unsaved edits when an account is linked', async () => {
  vi.mocked(api.post).mockResolvedValue(undefined as never)
  render(UserEdit, { id: 5 })
  await waitFor(() => expect(screen.getByLabelText('Display name')).toBeTruthy())
  await fireEvent.input(screen.getByLabelText('Display name'), { target: { value: 'Robert' } })
  await fireEvent.input(screen.getByLabelText(t.admin.users.subject), { target: { value: 'sub-1' } })
  await fireEvent.click(screen.getByRole('button', { name: t.admin.users.link }))
  await waitFor(() => expect(api.post).toHaveBeenCalledWith('/admin/users/5/link', { subject: 'sub-1' }))
  await waitFor(() => expect(vi.mocked(api.get).mock.calls.filter((c) => c[0] === '/admin/users/5').length).toBe(2))
  expect((screen.getByLabelText('Display name') as HTMLInputElement).value).toBe('Robert')
})

it('reports a failed refresh after an unlink instead of staying silent', async () => {
  let linked = false
  vi.mocked(api.get).mockImplementation(async (p: string) => {
    if (p.endsWith('/paths') || p === '/admin/roots' || p === '/admin/ranking') return { items: [] } as never
    // The first read of the account succeeds and shows it as linked; the refresh then fails.
    if (!linked) {
      linked = true
      return { ...user, oidc_linked: true } as never
    }
    throw new Error('down')
  })
  vi.mocked(api.post).mockResolvedValue(undefined as never)
  render(UserEdit, { id: 5 })
  await fireEvent.click(await screen.findByRole('button', { name: t.admin.users.unlink }))
  await fireEvent.click(screen.getAllByRole('button', { name: t.admin.users.unlink }).at(-1)!)
  await waitFor(() => expect(api.post).toHaveBeenCalledWith('/admin/users/5/unlink'))
  await waitFor(() => expect(toasts.items.map((x) => x.text)).toContain(t.admin.users.refreshFailed))
})
