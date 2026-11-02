import { render, screen, waitFor } from '@testing-library/svelte'
import { beforeEach, expect, it, vi } from 'vitest'

vi.mock('uplot', () => ({ default: class { setData() {} setSize() {} destroy() {} } }))
vi.mock('uplot/dist/uPlot.min.css', () => ({}))
vi.mock('../../lib/api/client', async (orig) => {
  const m = await orig<typeof import('../../lib/api/client')>()
  return { ...m, api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() } }
})

import { api } from '../../lib/api/client'
import { t } from '../../lib/i18n/en'
import HistoryTab from './HistoryTab.svelte'

const chart = () => document.querySelector('.chart') as HTMLElement

beforeEach(() => {
  vi.mocked(api.get).mockReset()
  vi.stubGlobal('matchMedia', () => ({ matches: true }))
})

it("does not keep the previous folder's chart when the next folder's history fails", async () => {
  vi.mocked(api.get).mockResolvedValueOnce({ items: [{ ts: 1, bytes_alloc: 5 }, { ts: 2, bytes_alloc: 6 }] } as never)
  const { rerender } = render(HistoryTab, { ref: '1.1' })
  await waitFor(() => expect(chart().hidden).toBe(false))

  vi.mocked(api.get).mockRejectedValueOnce(new Error('down'))
  await rerender({ ref: '1.2' })
  await waitFor(() => expect(screen.getByText(t.history.failed)).toBeTruthy())
  expect(chart().hidden).toBe(true)
})
