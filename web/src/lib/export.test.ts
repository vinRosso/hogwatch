import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { ApiError } from './api/client'
import { startExport } from './export'

const assign = vi.fn()
beforeEach(() => {
  assign.mockReset()
  vi.stubGlobal('location', { ...window.location, assign })
})
afterEach(() => vi.unstubAllGlobals())

it('on 429 returns retryAfter and never navigates', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('{}', { status: 429, headers: { 'Retry-After': '30' } })))
  expect(await startExport('1.2', 'csv', 'tree')).toEqual({ retryAfter: 30 })
  expect(assign).not.toHaveBeenCalled()
})

it('on 204 clicks one download link for the export URL, after a check request', async () => {
  const f = vi.fn().mockResolvedValue(new Response(null, { status: 204 }))
  vi.stubGlobal('fetch', f)
  const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
  expect(await startExport('1.2', 'json', 'top')).toBe('started')
  expect(click).toHaveBeenCalledTimes(1)
  expect(assign).not.toHaveBeenCalled()
  expect(f).toHaveBeenCalledTimes(1)
  expect(f.mock.calls[0][0]).toBe('/api/v1/export/1.2?format=json&what=top&check=true')
})

it('throws ApiError for other statuses', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ title: 'Not found', detail: 'gone' }), { status: 404 })))
  await expect(startExport('1.2', 'csv', 'usage')).rejects.toBeInstanceOf(ApiError)
  expect(assign).not.toHaveBeenCalled()
})

it('downloads through a link click, never by navigating the page', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(null, { status: 204 })))
  const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
  await startExport('1.2', 'csv', 'tree')
  expect(click).toHaveBeenCalledTimes(1)
  expect(assign).not.toHaveBeenCalled()
})
