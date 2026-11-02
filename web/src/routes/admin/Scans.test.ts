import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import { beforeEach, expect, it, vi } from 'vitest'

vi.mock('../../lib/api/client', async (orig) => {
  const m = await orig<typeof import('../../lib/api/client')>()
  return { ...m, api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() } }
})

import { api, ApiError } from '../../lib/api/client'
import Mounts from './Mounts.svelte'
import Scans from './Scans.svelte'

const job = (status: string) => ({ job_id: 3, root_id: 1, status, scan_id: null, progress: null, error: null, created_at: 1, started_at: null, finished_at: null })

beforeEach(() => {
  for (const f of [api.get, api.post]) vi.mocked(f).mockReset()
})

it('stops polling the progress once the job is done', async () => {
  vi.useFakeTimers()
  try {
    let progressCalls = 0
    vi.mocked(api.get).mockImplementation(async (p: string) => {
      if (p === '/admin/roots') return { items: [{ id: 1, name: 'tank', schedule: '' }] } as never
      if (p === '/admin/scans') return { items: [], next_cursor: null } as never
      progressCalls++
      return job(progressCalls >= 2 ? 'done' : 'running') as never
    })
    vi.mocked(api.post).mockResolvedValue({ id: 3, status: 'queued' } as never)
    render(Scans)
    await vi.advanceTimersByTimeAsync(10)
    await fireEvent.click(screen.getByRole('button', { name: 'Scan now' }))
    await vi.advanceTimersByTimeAsync(10)
    await vi.advanceTimersByTimeAsync(2100)
    expect(progressCalls).toBe(2)
    await vi.advanceTimersByTimeAsync(10000)
    expect(progressCalls).toBe(2)
  } finally {
    vi.useRealTimers()
  }
})

it('shows the 409 detail when a scan is already running', async () => {
  vi.mocked(api.get).mockImplementation(async (p: string) => (p === '/admin/roots' ? { items: [{ id: 1, name: 'tank', schedule: '' }] } : { items: [], next_cursor: null }) as never)
  vi.mocked(api.post).mockRejectedValue(new ApiError(409, 'Conflict', 'a scan is already running', null))
  render(Scans)
  await waitFor(() => expect(screen.getByRole('button', { name: 'Scan now' })).toBeTruthy())
  await fireEvent.click(screen.getByRole('button', { name: 'Scan now' }))
  await waitFor(() => expect(screen.getByText('a scan is already running')).toBeTruthy())
})

it('the mount picker hides mounts that already have a root', async () => {
  vi.mocked(api.get).mockImplementation(async (p: string) =>
    (p === '/admin/roots'
      ? { items: [] }
      : {
          items: [
            { container_path: '/mnt/used', host_path: '/h/used', fstype: 'zfs', readonly: true, root_id: 4 },
            { container_path: '/mnt/free', host_path: '/h/free', fstype: 'zfs', readonly: false, root_id: null },
          ],
        }) as never,
  )
  render(Mounts)
  await waitFor(() => expect(screen.getByRole('button', { name: 'New root' })).toBeTruthy())
  await new Promise((r) => setTimeout(r, 20))
  await fireEvent.click(screen.getByRole('button', { name: 'New root' }))
  await waitFor(() => expect(screen.getByRole('option', { name: /\/mnt\/free/ })).toBeTruthy())
  expect(screen.queryByRole('option', { name: /\/mnt\/used/ })).toBeNull()
  expect(screen.getByRole('alert').textContent).toContain('writable')
})

it('resumes a scan that is already running and stops polling on a 404', async () => {
  vi.useFakeTimers()
  try {
    let polls = 0
    vi.mocked(api.get).mockImplementation(async (p: string) => {
      if (p === '/admin/roots') return { items: [{ id: 1, name: 'tank', schedule: '' }] } as never
      if (p === '/admin/scans') {
        return { items: [{ id: 9, job_id: 3, root_id: 1, status: 'running', trigger: 'manual', started_at: 1, finished_at: null, dirs: 0, files: 0, bytes_alloc: 0, error: null, error_count: 0, progress: null }], next_cursor: null } as never
      }
      polls++
      throw new ApiError(404, 'Not found', '', null)
    })
    render(Scans)
    await vi.advanceTimersByTimeAsync(10)
    await vi.advanceTimersByTimeAsync(10000)
    expect(polls).toBe(1)
    expect(screen.getByRole('button', { name: 'Scan now' })).toBeTruthy()
  } finally {
    vi.useRealTimers()
  }
})

const scan = (id: number, over: object = {}) => ({ id, job_id: null, root_id: 1, status: 'done', trigger: 'manual', started_at: 1000 - id, finished_at: 1065 - id, dirs: 1, files: 1, bytes_alloc: 1, error: null, error_count: 0, progress: null, ...over })

it('queries each page with a limit and the cursor of that page', async () => {
  vi.mocked(api.get).mockImplementation(async (p: string, q?: unknown) => {
    if (p === '/admin/roots') return { items: [{ id: 1, name: 'tank', schedule: '' }] } as never
    return ((q as { cursor?: string }).cursor === 'c2' ? { items: [scan(1)], next_cursor: null } : { items: [scan(3), scan(2)], next_cursor: 'c2' }) as never
  })
  render(Scans)
  await waitFor(() => expect(screen.getByText('Page 1')).toBeTruthy())
  const first = vi.mocked(api.get).mock.calls.find(([p]) => p === '/admin/scans')![1] as { cursor?: string; limit: number }
  expect(first.cursor).toBeUndefined()
  expect(first.limit).toBeGreaterThanOrEqual(5)
  expect((screen.getByRole('button', { name: 'Previous' }) as HTMLButtonElement).disabled).toBe(true)
  await fireEvent.click(screen.getByRole('button', { name: 'Next' }))
  await waitFor(() => expect(screen.getByText('Page 2')).toBeTruthy())
  expect((screen.getByRole('button', { name: 'Next' }) as HTMLButtonElement).disabled).toBe(true)
  await fireEvent.click(screen.getByRole('button', { name: 'Previous' }))
  await waitFor(() => expect(screen.getByText('Page 1')).toBeTruthy())
})

it('shows scan errors in a popup and a duration instead of a finished time', async () => {
  vi.mocked(api.get).mockImplementation(async (p: string) =>
    (p === '/admin/roots'
      ? { items: [{ id: 1, name: 'tank', schedule: '' }] }
      : { items: [scan(1, { error: 'walk failed', error_count: 2, started_at: 0, finished_at: 185 })], next_cursor: null }) as never,
  )
  render(Scans)
  await waitFor(() => expect(screen.getByText('3 min 05 s')).toBeTruthy())
  expect(screen.queryByText('walk failed')).toBeNull()
  await fireEvent.click(screen.getByRole('button', { name: 'Show errors (2)' }))
  expect(screen.getByText('walk failed')).toBeTruthy()
  expect(screen.getByText('2 entries could not be read during this scan.')).toBeTruthy()
})
