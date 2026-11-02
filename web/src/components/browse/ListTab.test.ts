import { render, screen, waitFor } from '@testing-library/svelte'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('../../lib/api/client', async (orig) => {
  const m = await orig<typeof import('../../lib/api/client')>()
  return { ...m, api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() } }
})

import { api } from '../../lib/api/client'
import type { Child } from '../../lib/api/types'
import VirtualList from '../ui/VirtualList.svelte'
import ListTab from './ListTab.svelte'
import { createRawSnippet } from 'svelte'

const row = (name: string): Child => ({ name, kind: 'dir', ref: '1.2', files: 3, bytes_logical: 10, bytes_alloc: 10, mtime: 1000 })

beforeEach(() => vi.mocked(api.get).mockReset())

describe('ListTab', () => {
  it('renders hostile names as visible text, never as markup', async () => {
    const names = ['a\nb', 'x‮y', '<img src=x onerror=alert(1)>', 'bad\\xffbyte', '=cmd']
    vi.mocked(api.get).mockResolvedValue({ items: names.map(row), next_cursor: null } as never)
    const { container } = render(ListTab, { ref: '1.1', filter: '' })
    await waitFor(() => expect(screen.getByText('a␊b')).toBeTruthy())
    expect(screen.getByText('x\\u{202E}y')).toBeTruthy()
    expect(screen.getByText('<img src=x onerror=alert(1)>')).toBeTruthy()
    expect(screen.getByText('bad\\xffbyte')).toBeTruthy()
    expect(container.querySelector('img')).toBeNull()
    const cells = [...container.querySelectorAll('.name')]
    expect(cells.length).toBe(names.length)
    for (const c of cells) expect(c.getAttribute('style')).toContain('unicode-bidi: isolate')
  })

  it('has no growth column: growth is per root only', async () => {
    vi.mocked(api.get).mockResolvedValue({ items: [row('a')], next_cursor: null } as never)
    render(ListTab, { ref: '1.1', filter: '' })
    await waitFor(() => expect(screen.getByText('a')).toBeTruthy())
    expect(screen.queryByText(/growth/i)).toBeNull()
  })

  it('pages with limit 200 and the cursor', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ items: [row('a')], next_cursor: 'c1' } as never)
    vi.mocked(api.get).mockResolvedValue({ items: [row('b')], next_cursor: null } as never)
    render(ListTab, { ref: '1.1', filter: 'cat:video' })
    await waitFor(() => expect(screen.getByText('a')).toBeTruthy())
    const first = vi.mocked(api.get).mock.calls[0]
    expect(first[0]).toBe('/nodes/1.1/children')
    expect(first[1]).toMatchObject({ sort: 'size', limit: 200, filter: 'cat:video' })
    await waitFor(() => expect(vi.mocked(api.get).mock.calls.length).toBeGreaterThan(1))
    expect(vi.mocked(api.get).mock.calls[1][1]).toMatchObject({ cursor: 'c1' })
  })
})

describe('VirtualList', () => {
  it('keeps the DOM small for a million rows', () => {
    const rowSnippet = createRawSnippet((i: () => number) => ({ render: () => `<span class="x">row ${i()}</span>` }))
    const { container } = render(VirtualList, { count: 1_000_000, row: rowSnippet })
    expect(container.querySelectorAll('[role=listitem]').length).toBeLessThan(60)
    expect(container.querySelectorAll('[role=listitem]').length).toBeGreaterThan(0)
  })
})
