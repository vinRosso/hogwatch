import { beforeEach, expect, it, vi } from 'vitest'

vi.mock('./api/client', async (orig) => {
  const m = await orig<typeof import('./api/client')>()
  return { ...m, api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() } }
})

import { api } from './api/client'
import { loadOwners, ownerOf } from './owners.svelte'
import { session } from './session.svelte'

beforeEach(() => vi.mocked(api.get).mockReset())

it('an admin reads every assignment in one request, however many users there are', async () => {
  session.me = { id: 1, is_admin: true } as never
  vi.mocked(api.get).mockResolvedValue({ items: [{ user_id: 7, root_id: 1, rel_path: 'a' }, { user_id: 250, root_id: 1, rel_path: 'a/b' }] } as never)
  await loadOwners()
  expect(vi.mocked(api.get).mock.calls.map((c) => c[0])).toEqual(['/admin/user-paths'])
  expect(ownerOf(1, 'a/b/c')).toBe(250)
  expect(ownerOf(1, 'a/x')).toBe(7)
})
