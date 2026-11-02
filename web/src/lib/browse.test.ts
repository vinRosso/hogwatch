import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('./api/client', async (orig) => {
  const m = await orig<typeof import('./api/client')>()
  return { ...m, api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() } }
})

import { api, ApiError } from './api/client'
import type { Me, Node, RootInfo } from './api/types'
import { browse, crumbs } from './browse.svelte'
import { t } from './i18n/en'
import { navigate, router } from './router.svelte'
import { session } from './session.svelte'
import { toasts } from './toast.svelte'

const node = (ref: string, rel: string): Node => ({
  ref,
  root_id: 1,
  name: rel.split('/').pop() ?? '',
  rel_path: rel,
  parent_ref: null,
  files: 1,
  dirs: 0,
  bytes_logical: 1,
  bytes_alloc: 1,
  mtime: 0,
  flags: 0,
  error_count: 0,
})

const nodeIn = (ref: string, rel: string, parent: string | null): Node => ({ ...node(ref, rel), parent_ref: parent })

const root = (over: Partial<RootInfo> = {}): RootInfo => ({
  id: 1,
  name: 'storage',
  last_scanned_at: 1000,
  scanning: false,
  fs_total: null,
  fs_free: null,
  anchors: [{ ref: '5.1', host_path: '/srv/storage/alice', found: true }],
  ...over,
})

const gets = () => vi.mocked(api.get).mock.calls
const notFound = () => new ApiError(404, 'Not Found', '', null)

beforeEach(() => {
  vi.mocked(api.get).mockReset()
  session.roots = [root()]
  session.rootId = 1
  browse.notice = null
  navigate('/browse')
  toasts.items.length = 0
})

describe('stale ref recovery', () => {
  it('turns one 404 into one /resolve call and replaces the URL', async () => {
    vi.mocked(api.get).mockImplementation(async (path: string) => {
      if (path === '/nodes/5.8') return nodeIn('5.8', 'media/x', '5.9') as never
      if (path === '/nodes/5.9') throw notFound()
      if (path === '/resolve') return { ref: '6.9' } as never
      if (path === '/nodes/6.9') return node('6.9', 'media') as never
      throw new Error(path)
    })
    await browse.loadNode('5.8')
    await browse.loadNode('5.9')
    const resolves = gets().filter((c) => c[0] === '/resolve')
    expect(resolves).toHaveLength(1)
    expect(resolves[0][1]).toEqual({ root: 1, path: 'media' })
    expect(router.path).toBe('/browse/6.9')
    expect(browse.node?.ref).toBe('6.9')
    expect(browse.notice).toBeNull()
  })

  it('falls back to the first anchor with a notice when /resolve 404s too, without looping', async () => {
    vi.mocked(api.get).mockImplementation(async (path: string) => {
      if (path === '/nodes/5.8') return nodeIn('5.8', 'media/x', '5.9') as never
      if (path === '/nodes/5.9') throw notFound()
      if (path === '/resolve') throw notFound()
      if (path === '/nodes/5.1') return node('5.1', 'alice') as never
      throw new Error(path)
    })
    await browse.loadNode('5.8')
    await browse.loadNode('5.9')
    expect(gets().filter((c) => c[0] === '/resolve')).toHaveLength(1)
    expect(browse.notice).toBe(t.browse.moved)
    expect(router.path).toBe('/browse/5.1')
    expect(browse.node?.ref).toBe('5.1')
  })

  it("never resolves another node's path for a stale ref it has not seen", async () => {
    vi.mocked(api.get).mockImplementation(async (path: string) => {
      if (path === '/nodes/5.8') return nodeIn('5.8', 'media/x', '5.7') as never
      if (path === '/nodes/9.9') throw notFound()
      if (path === '/roots') return { items: [root()] } as never
      if (path === '/nodes/5.1') return node('5.1', 'alice') as never
      throw new Error(path)
    })
    await browse.loadNode('5.8')
    await browse.loadNode('9.9')
    expect(gets().filter((c) => c[0] === '/resolve')).toHaveLength(0)
    expect(browse.notice).toBe(t.browse.moved)
    expect(browse.node?.ref).toBe('5.1')
  })

  it('stops after one retry when even the start folder is gone', async () => {
    vi.mocked(api.get).mockImplementation(async () => {
      throw notFound()
    })
    await browse.loadNode('5.9')
    expect(gets().length).toBeLessThan(5)
    expect(browse.status).toBe('error')
  })
})

describe('degenerate scopes', () => {
  it('a user with no anchors gets a calm empty state', async () => {
    session.roots = [root({ anchors: [] })]
    await browse.start(null)
    expect(browse.status).toBe('empty')
    expect(browse.emptyReason).toBe(t.browse.noPaths)
    expect(gets().filter((c) => c[0] !== '/roots')).toHaveLength(0)
  })

  it('an anchor with ref null names its path', async () => {
    session.roots = [root({ anchors: [{ ref: null, host_path: '/srv/storage/gone\\xff', found: false }] })]
    await browse.start(null)
    expect(browse.emptyReason).toBe(t.browse.notInScan('/srv/storage/gone\\xff'))
    expect(gets().filter((c) => c[0] !== '/roots')).toHaveLength(0)
  })

  it('a root never scanned says so', async () => {
    session.roots = [root({ last_scanned_at: null })]
    await browse.start(null)
    expect(browse.emptyReason).toBe(t.browse.neverScanned)
  })
})

describe('roots that changed since sign-in', () => {
  it('re-reads the roots before picking the start state', async () => {
    session.roots = [root({ last_scanned_at: null, anchors: [{ ref: null, host_path: '/srv/storage/alice', found: false }] })]
    vi.mocked(api.get).mockImplementation(async (path: string) => {
      if (path === '/roots') return { items: [root()] } as never
      if (path === '/nodes/5.1') return node('5.1', 'alice') as never
      throw new Error(path)
    })
    await browse.start(null)
    expect(browse.status).toBe('idle')
    expect(browse.node?.ref).toBe('5.1')
  })
})

describe('URL query', () => {
  it('drops an invalid filter with a toast and no request', () => {
    browse.applyQuery({ filter: 'cat:bogus' })
    expect(toasts.items).toHaveLength(1)
    expect(browse.filter).toEqual({ cat: [], age: [], size: [] })
    expect(gets()).toHaveLength(0)
  })
  it('reads a valid filter and the depth', () => {
    browse.applyQuery({ filter: 'cat:video', depth: '3' })
    expect(browse.filter.cat).toEqual(['video'])
    expect(browse.depth).toBe(3)
  })
  it('reads depths up to 5 and falls back to 2 outside the range', () => {
    browse.applyQuery({ depth: '5' })
    expect(browse.depth).toBe(5)
    browse.applyQuery({ depth: '6' })
    expect(browse.depth).toBe(2)
  })
  it('takes the account depth for a URL without one; the URL still wins', () => {
    session.me = { prefs: { depth: 4 } } as Me
    browse.applyQuery({})
    expect(browse.depth).toBe(4)
    browse.applyQuery({ depth: '3' })
    expect(browse.depth).toBe(3)
    session.me = { prefs: { depth: 9 } } as Me
    browse.applyQuery({})
    expect(browse.depth).toBe(2)
    session.me = null
  })
  it('saves a picked depth to the account and clears it from the URL', () => {
    vi.mocked(api.patch).mockResolvedValue({})
    session.me = { prefs: {} } as Me
    navigate('/browse/1.2?depth=3')
    browse.setDepth(4)
    expect(browse.depth).toBe(4)
    expect(session.me.prefs).toEqual({ depth: 4 })
    expect(api.patch).toHaveBeenCalledWith('/me/prefs', { depth: 4 })
    expect(router.query.depth).toBeUndefined()
    session.me = null
  })
})

describe('crumbs', () => {
  it('labels the first crumb with the anchor host path and hides ancestors', () => {
    const out = crumbs('alice/media/movies', [{ ref: '5.1', host_path: '/srv/storage/alice', found: true }], 'storage')
    expect(out).toEqual([
      { label: '/storage/alice', rel: 'alice' },
      { label: 'media', rel: 'alice/media' },
      { label: 'movies', rel: 'alice/media/movies' },
    ])
  })
  it('shows hostile names as text', () => {
    const out = crumbs('a\nb', [], 'storage')
    expect(out.map((c) => c.label)).toEqual(['storage', 'a␊b'])
  })
})

describe('overlapping loads', () => {
  it('ignores a slow load that finishes after a newer one', async () => {
    let releaseA!: (n: Node) => void
    vi.mocked(api.get).mockImplementation((p: string) => {
      if (p === '/nodes/5.1') return new Promise<Node>((r) => (releaseA = r)) as never
      return Promise.resolve(node('5.2', 'b')) as never
    })
    const a = browse.loadNode('5.1')
    await browse.loadNode('5.2')
    releaseA(node('5.1', 'a'))
    await a
    expect(browse.node?.ref).toBe('5.2')
  })
})
