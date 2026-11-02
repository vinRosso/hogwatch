import { expect, it } from 'vitest'
import { errnoMessage } from './errno'

it('maps known errnos', () => {
  expect(errnoMessage(13)).toBe('permission denied')
  expect(errnoMessage(18)).toBe('other filesystem, skipped')
  expect(errnoMessage(99)).toBe('errno 99')
})
