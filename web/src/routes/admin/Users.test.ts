import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import { beforeEach, expect, it, vi } from 'vitest'

vi.mock('../../lib/api/client', async (orig) => {
  const m = await orig<typeof import('../../lib/api/client')>()
  return { ...m, api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() } }
})

import { api } from '../../lib/api/client'
import { t } from '../../lib/i18n/en'
import { router } from '../../lib/router.svelte'
import { session } from '../../lib/session.svelte'
import Users from './Users.svelte'

const u = (id: number, name: string) => ({ id, username: name, display_name: name, email: null, admin_local: false, admin_oidc: false, can_list_files: false, quota_bytes: null, disabled: false, oidc_linked: false, has_password: true, created_at: 1, last_login_at: null })

const rank = (id: number, name: string, alloc: number) => ({ user_id: id, username: name, bytes_alloc: alloc, shared_hardlink_bytes: 0, quota_bytes: null, quota_pct: null, growth_bytes: null })

beforeEach(() => {
  vi.mocked(api.get).mockReset()
  vi.mocked(api.patch).mockReset()
  vi.mocked(api.get).mockImplementation(async (p: string) =>
    (p === '/admin/users'
      ? { items: [u(1, 'me'), u(2, 'bob')], next_cursor: null }
      : p === '/admin/ranking'
        ? { items: [rank(2, 'bob', 9), rank(1, 'me', 1)] }
        : { items: [] }) as never,
  )
  router.path = '/admin/users'
  router.query = {}
  session.me = { id: 1 } as never
})

it('lists every account in ranking order, loading all user pages', async () => {
  vi.mocked(api.get).mockImplementation(async (p: string, q?: Record<string, unknown>) =>
    (p === '/admin/users'
      ? q?.cursor === 'c2'
        ? { items: [u(3, 'carol')], next_cursor: null }
        : { items: [u(1, 'me'), u(2, 'bob')], next_cursor: 'c2' }
      : p === '/admin/ranking'
        ? { items: [rank(3, 'carol', 50), rank(2, 'bob', 9), rank(1, 'me', 1)] }
        : { items: [] }) as never,
  )
  render(Users)
  await waitFor(() => expect(screen.getByText('carol')).toBeTruthy())
  const names = () => screen.getAllByRole('row').slice(1).map((r) => r.querySelector('td')!.textContent)
  expect(names()).toEqual(['carol', 'bob', 'me'])
  await fireEvent.click(screen.getByRole('button', { name: 'Sort by User' }))
  expect(router.query).toEqual({ sort: 'user' })
})

it('asks the ranking for window and root, and omits root for all roots', async () => {
  router.query = { window: '90d', root: '2' }
  render(Users)
  await waitFor(() => expect(vi.mocked(api.get).mock.calls.some((c) => c[0] === '/admin/ranking')).toBe(true))
  const first = vi.mocked(api.get).mock.calls.find((c) => c[0] === '/admin/ranking')!
  expect(first[1]).toEqual({ sort: 'usage', window: '90d', root: 2 })
  vi.mocked(api.get).mockClear()
  router.query = { window: '90d' }
  await waitFor(() => expect(vi.mocked(api.get).mock.calls.some((c) => c[0] === '/admin/ranking')).toBe(true))
  const second = vi.mocked(api.get).mock.calls.find((c) => c[0] === '/admin/ranking')!
  expect(second[1]).toEqual({ sort: 'usage', window: '90d', root: undefined })
})

it('creates a user with the quota in the chosen unit', async () => {
  vi.mocked(api.post).mockReset()
  vi.mocked(api.post).mockResolvedValue(u(9, 'dan') as never)
  render(Users)
  await waitFor(() => expect(screen.getByText('bob')).toBeTruthy())
  await fireEvent.click(screen.getByRole('button', { name: t.admin.users.new }))
  await fireEvent.input(screen.getByLabelText('Quota amount'), { target: { value: '1.5' } })
  await fireEvent.change(screen.getByLabelText('Quota unit'), { target: { value: 'TB' } })
  await fireEvent.click(screen.getByRole('button', { name: t.admin.users.create }))
  await waitFor(() => expect(api.post).toHaveBeenCalled())
  expect(vi.mocked(api.post).mock.calls[0][1]).toMatchObject({ quota_bytes: 1_500_000_000_000 })
})

it('changes a role from the dropdown, asks before disabling, and locks your own row', async () => {
  vi.mocked(api.patch).mockResolvedValue({ ...u(2, 'bob'), can_list_files: true } as never)
  render(Users)
  await waitFor(() => expect(screen.getByText('bob')).toBeTruthy())
  expect((screen.getByLabelText('Role of me') as HTMLSelectElement).disabled).toBe(true)
  const sel = screen.getByLabelText('Role of bob') as HTMLSelectElement
  await fireEvent.change(sel, { target: { value: 'lister' } })
  await waitFor(() => expect(api.patch).toHaveBeenCalledWith('/admin/users/2', { disabled: false, admin_local: false, can_list_files: true }))
  vi.mocked(api.patch).mockClear()
  vi.mocked(api.patch).mockResolvedValue({ ...u(2, 'bob'), disabled: true } as never)
  await fireEvent.change(sel, { target: { value: 'disabled' } })
  expect(api.patch).not.toHaveBeenCalled()
  expect(screen.getByText(/Disable bob\?/)).toBeTruthy()
  await fireEvent.click(screen.getAllByRole('button', { name: 'Disable' }).at(-1)!)
  await waitFor(() => expect(api.patch).toHaveBeenCalledWith('/admin/users/2', { disabled: true }))
})

it('puts the dropdown back to the old role when the change is refused', async () => {
  vi.mocked(api.patch).mockRejectedValue(new Error('refused'))
  render(Users)
  await waitFor(() => expect(screen.getByText('bob')).toBeTruthy())
  const sel = screen.getByLabelText('Role of bob') as HTMLSelectElement
  await fireEvent.change(sel, { target: { value: 'lister' } })
  await waitFor(() => expect(screen.getByRole('alert')).toBeTruthy())
  expect(sel.value).toBe('user')
})
