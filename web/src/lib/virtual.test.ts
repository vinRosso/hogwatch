import { expect, it } from 'vitest'
import { visibleRange } from './virtual'

it('renders the viewport plus overscan', () => {
  expect(visibleRange(0, 640, 32, 1_000_000)).toEqual({ start: 0, end: 30 })
  expect(visibleRange(32_000, 640, 32, 1_000_000)).toEqual({ start: 990, end: 1030 })
})

it('clamps to the row count', () => {
  expect(visibleRange(0, 640, 32, 5)).toEqual({ start: 0, end: 5 })
  expect(visibleRange(0, 640, 32, 0)).toEqual({ start: 0, end: 0 })
})
