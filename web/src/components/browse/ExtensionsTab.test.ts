import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import { expect, it, vi } from 'vitest'

vi.mock('../../lib/api/client', async (orig) => {
  const m = await orig<typeof import('../../lib/api/client')>()
  return { ...m, api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() } }
})

import { api } from '../../lib/api/client'
import { session } from '../../lib/session.svelte'
import ExtensionsTab from './ExtensionsTab.svelte'

it('lists the stored totals', async () => {
  vi.mocked(api.get).mockResolvedValue({
    items: [{ ext: 'mkv', category: 'video', files: 3, bytes_alloc: 6_000_000_000 }],
  } as never)
  render(ExtensionsTab, { ref: '1.1', files: 3 })
  await waitFor(() => expect(screen.getByText('mkv')).toBeTruthy())
})

it('says the totals are not stored for a folder with files', async () => {
  vi.mocked(api.get).mockResolvedValue({ items: [] } as never)
  render(ExtensionsTab, { ref: '1.1', files: 12 })
  await waitFor(() => expect(screen.getByText(/only kept for large folders/)).toBeTruthy())
  expect(screen.queryByRole('button', { name: 'List files' })).toBeNull()
})

it('offers to list the files to a user who may', async () => {
  session.me = { can_list_files: true } as never
  vi.mocked(api.get).mockResolvedValue({ items: [] } as never)
  const onlist = vi.fn()
  render(ExtensionsTab, { ref: '1.7', files: 12, onlist })
  await fireEvent.click(await screen.findByRole('button', { name: 'List files' }))
  expect(onlist).toHaveBeenCalledWith('1.7')
  session.me = null
})

it('offers it to an admin, who may list files implicitly', async () => {
  session.me = { can_list_files: false, is_admin: true } as never
  vi.mocked(api.get).mockResolvedValue({ items: [] } as never)
  render(ExtensionsTab, { ref: '1.7', files: 12, onlist: vi.fn() })
  expect(await screen.findByRole('button', { name: 'List files' })).toBeTruthy()
  session.me = null
})

it('hides the button from a user who may not list files', async () => {
  session.me = { can_list_files: false, is_admin: false } as never
  vi.mocked(api.get).mockResolvedValue({ items: [] } as never)
  render(ExtensionsTab, { ref: '1.7', files: 12, onlist: vi.fn() })
  await waitFor(() => expect(screen.getByText(/only kept for large folders/)).toBeTruthy())
  expect(screen.queryByRole('button', { name: 'List files' })).toBeNull()
  session.me = null
})

it('says "No files." only for an empty folder', async () => {
  vi.mocked(api.get).mockResolvedValue({ items: [] } as never)
  render(ExtensionsTab, { ref: '1.1', files: 0 })
  await waitFor(() => expect(screen.getByText('No files.')).toBeTruthy())
})
