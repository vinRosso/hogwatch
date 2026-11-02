import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { api, ApiError, setUnauthorizedHandler } from './client'

const fetchMock = vi.fn()
beforeEach(() => vi.stubGlobal('fetch', fetchMock))
afterEach(() => {
  fetchMock.mockReset()
  setUnauthorizedHandler(() => {})
})

const json = (status: number, body: unknown, headers: Record<string, string> = {}) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json', ...headers } })

it('POST sends the CSRF header and JSON content type; GET sends neither', async () => {
  fetchMock.mockImplementation(() => Promise.resolve(json(200, {})))
  await api.post('/x', { a: 1 })
  const [, init] = fetchMock.mock.calls[0]
  expect(init.headers['X-Hogwatch-CSRF']).toBe('1')
  expect(init.headers['Content-Type']).toBe('application/json')
  expect(init.body).toBe('{"a":1}')
  await api.get('/y', { q: 1, skip: undefined })
  const [u, g] = fetchMock.mock.calls[1]
  expect(u).toBe('/api/v1/y?q=1')
  expect(g.headers['X-Hogwatch-CSRF']).toBeUndefined()
  expect(g.headers['Content-Type']).toBeUndefined()
})

it('DELETE is a mutation without a body', async () => {
  fetchMock.mockResolvedValue(new Response(null, { status: 204 }))
  expect(await api.del('/z')).toBeUndefined()
  expect(fetchMock.mock.calls[0][1].headers['X-Hogwatch-CSRF']).toBe('1')
  expect(fetchMock.mock.calls[0][1].headers['Content-Type']).toBeUndefined()
})

it('a 429 carries Retry-After seconds', async () => {
  fetchMock.mockResolvedValue(json(429, { title: 'Too many', status: 429 }, { 'Retry-After': '7' }))
  const e = (await api.get('/x').catch((e) => e)) as ApiError
  expect(e).toBeInstanceOf(ApiError)
  expect(e.retryAfter).toBe(7)
  expect(e.status).toBe(429)
})

it('a 401 calls the hook once', async () => {
  const hook = vi.fn()
  setUnauthorizedHandler(hook)
  fetchMock.mockResolvedValue(json(401, { title: 'Unauthorized', status: 401 }))
  await expect(api.get('/me')).rejects.toBeInstanceOf(ApiError)
  expect(hook).toHaveBeenCalledTimes(1)
})

it('keeps the detail of a problem document', async () => {
  fetchMock.mockResolvedValue(json(409, { title: 'Conflict', status: 409, detail: 'last admin' }))
  const e = (await api.patch('/x', {}).catch((e) => e)) as ApiError
  expect(e.detail).toBe('last admin')
  expect(e.title).toBe('Conflict')
})
