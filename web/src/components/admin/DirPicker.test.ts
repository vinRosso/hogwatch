import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import { beforeEach, expect, it, vi } from 'vitest'

vi.mock('../../lib/api/client', async (orig) => {
  const m = await orig<typeof import('../../lib/api/client')>()
  return { ...m, api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() } }
})

import { api } from '../../lib/api/client'
import { t } from '../../lib/i18n/en'
import DirPicker from './DirPicker.svelte'

const dir = (name: string) => ({ kind: 'dir', ref: `1.${name}`, name })

beforeEach(() => {
  vi.mocked(api.get).mockReset()
  vi.mocked(api.get).mockImplementation(async (p: string, q?: Record<string, unknown>) =>
    (p === '/resolve'
      ? { ref: '1.0' }
      : p === '/nodes/1.0'
        ? { ref: '1.0', rel_path: '', parent_ref: null }
        : q?.cursor === 'c2'
          ? { items: [dir('zeta')], next_cursor: null }
          : { items: [dir('alpha')], next_cursor: 'c2' }) as never,
  )
})

it('offers folders past the first page', async () => {
  render(DirPicker, { open: true, rootId: 1, onpick: () => {}, onclose: () => {} })
  await waitFor(() => expect(screen.getByText('alpha')).toBeTruthy())
  expect(screen.queryByText('zeta')).toBeNull()
  await fireEvent.click(screen.getByRole('button', { name: t.admin.paths.moreFolders }))
  await waitFor(() => expect(screen.getByText('zeta')).toBeTruthy())
  expect(screen.queryByRole('button', { name: t.admin.paths.moreFolders })).toBeNull()
})

it('keeps the folders it has and offers a retry when the next page fails', async () => {
  render(DirPicker, { open: true, rootId: 1, onpick: () => {}, onclose: () => {} })
  await waitFor(() => expect(screen.getByText('alpha')).toBeTruthy())
  const next = vi.mocked(api.get).getMockImplementation()!
  vi.mocked(api.get).mockImplementation(async (p: string, q?: Record<string, unknown>) => {
    if (q?.cursor === 'c2') throw new Error('down')
    return next(p, q as never) as never
  })
  await fireEvent.click(screen.getByRole('button', { name: t.admin.paths.moreFolders }))
  expect(await screen.findByText(t.admin.paths.moreFailed)).toBeTruthy()
  expect(screen.getByText('alpha')).toBeTruthy()
  expect(screen.queryByText(t.admin.paths.noScan)).toBeNull()
  expect((screen.getByRole('button', { name: t.admin.paths.moreFolders }) as HTMLButtonElement).disabled).toBe(false)
})
