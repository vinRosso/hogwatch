import { render, screen, waitFor } from '@testing-library/svelte'
import { expect, it, vi } from 'vitest'

vi.mock('../../lib/api/client', async (orig) => {
  const m = await orig<typeof import('../../lib/api/client')>()
  return { ...m, api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() } }
})

import { api } from '../../lib/api/client'
import ErrorsTab from './ErrorsTab.svelte'

it('maps errno 13 to "permission denied" and shows names as text', async () => {
  vi.mocked(api.get).mockResolvedValue({
    items: [
      { name: 'secret\ndir', errno: 13 },
      { name: 'gone', errno: 2 },
    ],
    next_cursor: null,
  } as never)
  render(ErrorsTab, { ref: '1.1' })
  await waitFor(() => expect(screen.getByText('permission denied')).toBeTruthy())
  expect(screen.getByText('secret␊dir')).toBeTruthy()
  expect(screen.getByText('vanished during the scan')).toBeTruthy()
})

it('says so when there are none', async () => {
  vi.mocked(api.get).mockResolvedValue({ items: [], next_cursor: null } as never)
  render(ErrorsTab, { ref: '1.1' })
  await waitFor(() => expect(screen.getByText('No scan errors under this folder.')).toBeTruthy())
})
