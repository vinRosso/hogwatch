import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import { beforeEach, expect, it, vi } from 'vitest'

vi.mock('../../lib/api/client', async (orig) => {
  const m = await orig<typeof import('../../lib/api/client')>()
  return { ...m, api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() } }
})

import { api } from '../../lib/api/client'
import Mounts from './Mounts.svelte'
import Settings from './Settings.svelte'
import { router } from '../../lib/router.svelte'

beforeEach(() => {
  for (const f of [api.get, api.put]) vi.mocked(f).mockReset()
})

it('PUT sends every key, not only the changed one', async () => {
  vi.mocked(api.get).mockResolvedValue({ a: '1', b: '2' } as never)
  vi.mocked(api.put).mockResolvedValue({ a: '1', b: '3' } as never)
  render(Settings)
  await waitFor(() => expect(screen.getByLabelText('b')).toBeTruthy())
  await fireEvent.input(screen.getByLabelText('b'), { target: { value: '3' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() => expect(api.put).toHaveBeenCalled())
  expect(vi.mocked(api.put).mock.calls[0]).toEqual(['/admin/settings', { a: '1', b: '3' }])
})

it('the mount card joins the root with its figures and links to its users', async () => {
  vi.mocked(api.get).mockImplementation(async (p: string) =>
    (p === '/admin/roots'
      ? { items: [{ id: 2, name: 'tank', container_path: '/scan/tank', host_path: '/tank', cross_fs: false, excludes: [], schedule: '', enabled: true, zfs_dataset: null, current_scan_id: 7 }] }
      : p === '/admin/mounts'
        ? { items: [{ root_id: 2, root_name: 'tank', bytes_alloc: 5, growth_bytes: null, fs_total: null, fs_free: null, zfs_used: null, zfs_avail: null, zfs_quota: null, zfs_used_snapshots: null }] }
        : { items: [] }) as never,
  )
  router.query = {}
  render(Mounts)
  await waitFor(() => expect(screen.getByRole('button', { name: 'Edit' })).toBeTruthy())
  expect(screen.getByRole('heading', { name: 'tank' })).toBeTruthy()
  expect(screen.getByText('#7')).toBeTruthy()
  expect(screen.getByText('5 B')).toBeTruthy()
})
