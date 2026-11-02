import { render, screen, waitFor } from '@testing-library/svelte'
import { beforeEach, expect, it, vi } from 'vitest'

vi.mock('../../lib/api/client', async (orig) => {
  const m = await orig<typeof import('../../lib/api/client')>()
  return { ...m, api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() } }
})

import { api } from '../../lib/api/client'
import Issues from './Issues.svelte'

beforeEach(() => vi.mocked(api.get).mockReset())

it('links node refs of a writable_mount issue to Browse and marks derived issues', async () => {
  vi.mocked(api.get).mockImplementation(async (p: string) =>
    (p === '/admin/roots'
      ? { items: [{ id: 1, name: 'tank' }] }
      : {
          items: [
            { id: 4, root_id: 1, kind: 'writable_mount', details: { nodes: ['7.3'], count: 1 }, created_at: 1 },
            { id: null, root_id: 1, kind: 'overlap', details: { note: '<b>x</b>' }, created_at: 1 },
          ],
        }) as never,
  )
  const { container } = render(Issues)
  await waitFor(() => expect(screen.getByText('7.3')).toBeTruthy())
  expect(screen.getByText('7.3').closest('a')?.getAttribute('href')).toBe('/browse/7.3')
  expect(screen.getByText('computed now')).toBeTruthy()
  expect(screen.getByText('<b>x</b>')).toBeTruthy()
  expect(container.querySelector('b')).toBeNull()
})
