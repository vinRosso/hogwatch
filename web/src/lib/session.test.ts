import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { setUnauthorizedHandler } from './api/client'
import type { MyUsage } from './api/types'
import { navigate, router } from './router.svelte'
import { installAuthRedirect, session } from './session.svelte'

function usage(over: Partial<MyUsage>): MyUsage {
  return {
    quota_bytes: 2e12,
    quota_basis: 'allocated',
    total: { files: 1, bytes_logical: 5e11, bytes_alloc: 1.82e12 },
    growth_bytes: null,
    mounts: [],
    paths: [],
    ...over,
  }
}

describe('session.quota()', () => {
  it('reports pct and warning levels on the allocated basis', () => {
    session.usage = usage({})
    expect(session.quota()).toMatchObject({ pct: 91, level: 'warn80' })
    session.usage = usage({ total: { files: 1, bytes_logical: 0, bytes_alloc: 1.9e12 } })
    expect(session.quota()?.level).toBe('warn95')
    session.usage = usage({ total: { files: 1, bytes_logical: 0, bytes_alloc: 1e12 } })
    expect(session.quota()?.level).toBe('ok')
  })
  it('is null without a quota and ignores a logical quota_basis', () => {
    session.usage = usage({ quota_bytes: null })
    expect(session.quota()).toBeNull()
    session.usage = usage({ quota_basis: 'logical' })
    expect(session.quota()).toMatchObject({ used: 1.82e12, pct: 91, level: 'warn80' })
  })
})

describe('401 redirect', () => {
  beforeEach(() => {
    vi.stubGlobal('fetch', () =>
      Promise.resolve(new Response(JSON.stringify({ title: 'Unauthorized', status: 401 }), { status: 401 })),
    )
    installAuthRedirect()
  })
  afterEach(() => setUnauthorizedHandler(() => {}))

  it('sends the browser to /login?next=<current path>', async () => {
    navigate('/browse/1.2?filter=cat:video')
    await session.load()
    expect(router.path).toBe('/login')
    expect(router.query.next).toBe('/browse/1.2?filter=cat:video')
    expect(session.me).toBeNull()
  })

  it('does not loop when already on /login', async () => {
    navigate('/login?next=%2Fusage')
    await session.load()
    expect(router.query.next).toBe('/usage')
  })
})

describe('load failures', () => {
  it('a 500 sets loadError instead of throwing or spinning forever', async () => {
    vi.stubGlobal('fetch', () => Promise.resolve(new Response('{}', { status: 500 })))
    navigate('/browse')
    await expect(session.load()).resolves.toBeUndefined()
    expect(session.loadError).not.toBeNull()
    expect(session.me).toBeNull()
    expect(router.path).toBe('/browse')
  })
})
