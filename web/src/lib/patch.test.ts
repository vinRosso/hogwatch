import { expect, it } from 'vitest'
import { checkRelPath, diffPatch } from './patch'

it('keeps an explicit null', () => {
  expect(diffPatch({ quota_bytes: 5 as number | null }, { quota_bytes: null }, ['quota_bytes'])).toEqual({ quota_bytes: null })
})
it('is empty when nothing changed', () => {
  expect(diffPatch({ a: 1, b: 'x' }, { a: 1, b: 'x' }, ['a', 'b'])).toEqual({})
})
it('omits an empty password and keeps a typed one', () => {
  expect(diffPatch({ password: '' }, { password: '' }, ['password'])).toEqual({})
  expect(diffPatch({ password: '' }, { password: 'hunter2hunter2' }, ['password'])).toEqual({ password: 'hunter2hunter2' })
})
it('ignores keys that are not listed', () => {
  expect(diffPatch({ a: 1, b: 1 }, { a: 2, b: 2 }, ['a'])).toEqual({ a: 2 })
})
it('rejects dot-dot, empty segments, dot segments and NUL', () => {
  expect(checkRelPath('a/../b')).toBe('dot_segment')
  expect(checkRelPath('a//b')).toBe('empty_segment')
  expect(checkRelPath('/a')).toBe('empty_segment')
  expect(checkRelPath('a/./b')).toBe('dot_segment')
  expect(checkRelPath('a\0b')).toBe('nul')
  expect(checkRelPath('media/tv shows')).toBeNull()
})
