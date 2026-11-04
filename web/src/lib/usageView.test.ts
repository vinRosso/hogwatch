import { describe, expect, it } from 'vitest'
import { share, sortPaths } from './usageView'

const row = (n: string, bytes: number, found = true, redundant = false) => ({ n, found, redundant, usage: { bytes_alloc: bytes } })

describe('share', () => {
  it('is the fraction, clamped, and zero without a whole', () => {
    expect(share(25, 100)).toBe(0.25)
    expect(share(150, 100)).toBe(1)
    expect(share(5, 0)).toBe(0)
  })
})

describe('sortPaths', () => {
  const rows = [row('b', 10), row('gone', 999, false), row('a', 10), row('c', 50), row('dup', 70, true, true)]
  const order = (by: 'size' | 'name') => sortPaths(rows, by, (p) => p.n).map((p) => p.n)

  it('puts the largest first and the flagged paths last', () => {
    expect(order('size')).toEqual(['c', 'a', 'b', 'gone', 'dup'])
  })
  it('sorts by name with the flagged paths last', () => {
    expect(order('name')).toEqual(['a', 'b', 'c', 'dup', 'gone'])
  })
  it('does not reorder its input', () => {
    order('size')
    expect(rows[0].n).toBe('b')
  })
})
