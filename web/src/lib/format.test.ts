import { describe, expect, it } from 'vitest'
import { formatAge, formatBytes, formatCount, formatDate, formatDateTime, formatDuration, parseBytes } from './format'

describe('formatBytes', () => {
  it.each([
    [0, '0 B'],
    [999, '999 B'],
    [1500, '1.50 kB'],
    [640e9, '640 GB'],
    [1.82e12, '1.82 TB'],
    [12_345_678, '12.3 MB'],
    [999_999, '1.00 MB'],
  ])('%d -> %s', (n, want) => expect(formatBytes(n)).toBe(want))
})

describe('parseBytes', () => {
  it('parses SI sizes', () => {
    expect(parseBytes('10 GB')).toBe(1e10)
    expect(parseBytes('1.5kB')).toBe(1500)
    expect(parseBytes('2048')).toBe(2048)
    expect(parseBytes('x')).toBeNull()
    expect(parseBytes('')).toBeNull()
  })
})

describe('counts and dates', () => {
  it('formats', () => {
    expect(formatCount(1234567)).toBe('1,234,567')
    expect(formatDate(0)).toBe('1970-01-01')
    expect(formatDateTime(3 * 3600 + 5 * 60 + 59)).toBe('1970-01-01 03:05')
    expect([0, 42, 185, 7620].map(formatDuration)).toEqual(['0 s', '42 s', '3 min 05 s', '2 h 07 min'])
    expect(formatAge(1000, 1030)).toBe('just now')
    expect(formatAge(1000, 1000 + 7200)).toBe('2 h ago')
    expect(formatAge(1000, 1000 + 3 * 86400)).toBe('3 d ago')
  })
})
