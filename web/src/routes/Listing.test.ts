import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('../lib/api/client', async (orig) => {
  const m = await orig<typeof import('../lib/api/client')>()
  return { ...m, api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() } }
})

import { api, ApiError } from '../lib/api/client'
import { router } from '../lib/router.svelte'
import { session } from '../lib/session.svelte'
import ListingDialog from '../components/browse/ListingDialog.svelte'
import Listing from './Listing.svelte'
import Usage from './Usage.svelte'

beforeEach(() => {
  for (const f of [api.get, api.post, api.del]) vi.mocked(f).mockReset()
})

const done = (over: object = {}) => ({ id: 7, status: 'done', truncated: false, error: null, items: [], next_cursor: null, ...over })

describe('Listing route', () => {
  it('shows the truncated banner', async () => {
    vi.mocked(api.get).mockResolvedValue(done({ truncated: true, items: [{ name: 'a', type: 'f', size: 1, alloc: 1, mtime: 1, uid: 0 }] }) as never)
    render(Listing, { id: 7 })
    await waitFor(() => expect(screen.getByText('Showing the first 100,000 entries')).toBeTruthy())
  })

  it('shows the fixed error text verbatim', async () => {
    vi.mocked(api.get).mockResolvedValue(done({ status: 'failed', error: 'changed since last scan' }) as never)
    render(Listing, { id: 7 })
    await waitFor(() => expect(screen.getByText('changed since last scan')).toBeTruthy())
  })

  it('says it expired on 404', async () => {
    vi.mocked(api.get).mockRejectedValue(new ApiError(404, 'Not found', '', null))
    render(Listing, { id: 7 })
    await waitFor(() => expect(screen.getByText(/has expired/)).toBeTruthy())
  })

  it('polls while running and stops once done', async () => {
    vi.useFakeTimers()
    try {
      vi.mocked(api.get)
        .mockResolvedValueOnce(done({ status: 'running' }) as never)
        .mockResolvedValue(done() as never)
      render(Listing, { id: 7 })
      await vi.advanceTimersByTimeAsync(0)
      expect(vi.mocked(api.get)).toHaveBeenCalledTimes(1)
      await vi.advanceTimersByTimeAsync(2100)
      expect(vi.mocked(api.get)).toHaveBeenCalledTimes(2)
      await vi.advanceTimersByTimeAsync(10000)
      expect(vi.mocked(api.get)).toHaveBeenCalledTimes(2)
    } finally {
      vi.useRealTimers()
    }
  })
})

describe('ListingDialog', () => {
  it('rejects a bad extension inline and does not post', async () => {
    render(ListingDialog, { open: true, ref: '1.2', onclose: () => {} })
    await fireEvent.input(screen.getByLabelText(/^Extensions/), { target: { value: 'MP4!' } })
    expect(screen.getByRole('alert').textContent).toContain('lower-case')
    await fireEvent.click(screen.getByRole('button', { name: 'Start listing' }))
    expect(api.post).not.toHaveBeenCalled()
  })

  it('posts the parsed request', async () => {
    vi.mocked(api.post).mockResolvedValue({ id: 9, status: 'queued' } as never)
    render(ListingDialog, { open: true, ref: '1.2', onclose: () => {} })
    await fireEvent.input(screen.getByLabelText(/^Extensions/), { target: { value: 'mp4, mkv' } })
    await fireEvent.input(screen.getByLabelText(/^Minimum size/), { target: { value: '10 GB' } })
    await fireEvent.input(screen.getByLabelText(/^Older than/), { target: { value: '30' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Start listing' }))
    await waitFor(() => expect(api.post).toHaveBeenCalled())
    expect(vi.mocked(api.post).mock.calls[0]).toEqual(['/listings', { ref: '1.2', mode: 'children', exts: ['mp4', 'mkv'], min_size: 1e10, min_age_days: 30 }])
  })

  it('opens in the mode it is given and posts it', async () => {
    vi.mocked(api.post).mockResolvedValue({ id: 9, status: 'queued' } as never)
    render(ListingDialog, { open: true, ref: '1.2', initialMode: 'subtree', onclose: () => {} })
    expect((screen.getByRole('radio', { name: /everything below/ }) as HTMLInputElement).checked).toBe(true)
    await fireEvent.click(screen.getByRole('button', { name: 'Start listing' }))
    await waitFor(() => expect(api.post).toHaveBeenCalled())
    expect(vi.mocked(api.post).mock.calls[0]).toEqual(['/listings', { ref: '1.2', mode: 'subtree' }])
  })

  it('shows the running-limit message on 429', async () => {
    vi.mocked(api.post).mockRejectedValue(new ApiError(429, 'Too many', '', 5))
    render(ListingDialog, { open: true, ref: '1.2', onclose: () => {} })
    await fireEvent.click(screen.getByRole('button', { name: 'Start listing' }))
    await waitFor(() => expect(screen.getByText('You already have 2 listings running')).toBeTruthy())
  })
})

describe('Usage route', () => {
  const usage = (over: object = {}) => ({
    quota_bytes: 100,
    quota_basis: 'allocated',
    total: { bytes_alloc: 50, bytes_logical: 60, files: 1, dirs: 1 },
    growth_bytes: 10,
    mounts: [{ root_id: 1, root_name: 'tank', usage: { bytes_alloc: 50, bytes_logical: 60, files: 1, dirs: 1 }, shared_hardlink_bytes: 1000, fs_total: null, fs_free: null, growth_bytes: null }],
    paths: [{ user_path_id: 1, root_id: 1, host_path: 'media/a\\xffb', found: true, redundant: false, usage: { bytes_alloc: 50, bytes_logical: 60, files: 1, dirs: 1 } }],
    ...over,
  })

  it('requests the window from the URL and shows a null growth as a dash', async () => {
    history.replaceState(null, '', '/usage?window=90d')
    router.path = '/usage'
    router.query = { window: '90d' }
    session.usage = usage() as never
    vi.mocked(api.get).mockResolvedValue(usage() as never)
    render(Usage)
    await waitFor(() => expect(screen.getByText('tank')).toBeTruthy())
    expect(vi.mocked(api.get).mock.calls[0]).toEqual(['/me/usage', { window: '90d' }])
    expect(screen.getByText('+ 1.00 kB in hardlinks shared with other paths')).toBeTruthy()
    expect(screen.getAllByText('—').length).toBeGreaterThan(0)
  })
})

describe('Listing polling robustness', () => {
  it('keeps polling after one failed poll', async () => {
    vi.useFakeTimers()
    try {
      vi.mocked(api.get)
        .mockResolvedValueOnce(done({ status: 'running' }) as never)
        .mockRejectedValueOnce(new ApiError(500, 'x', '', null))
        .mockResolvedValue(done() as never)
      render(Listing, { id: 7 })
      await vi.advanceTimersByTimeAsync(2100)
      await vi.advanceTimersByTimeAsync(2100)
      expect(vi.mocked(api.get).mock.calls.length).toBeGreaterThanOrEqual(3)
      await waitFor(() => expect(screen.getByText('Done')).toBeTruthy())
    } finally {
      vi.useRealTimers()
    }
  })
})
