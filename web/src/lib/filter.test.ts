import { describe, expect, it } from 'vitest'
import { ages, bucketRuns, filterToParam, parseFilterParam, type Filter } from './filter'

describe('bucketRuns', () => {
  it('groups contiguous buckets, in bucket order', () => {
    expect(bucketRuns(ages, [])).toEqual([])
    expect(bucketRuns(ages, ['lt1w', 'lt1mo', 'lt3mo'])).toEqual([[0, 2]])
    expect(bucketRuns(ages, ['ge10y', 'lt10y', 'lt2y'])).toEqual([[4, 4], [6, 7]])
    expect(bucketRuns(ages, ['lt1w', 'lt1w', 'nope'])).toEqual([[0, 0]])
  })
})

describe('filter param', () => {
  it('round trips and orders keys', () => {
    const f: Filter = { cat: ['video', 'audio'], age: ['lt1w'], size: ['ge10g'] }
    expect(filterToParam(f)).toBe('cat:video,audio;age:lt1w;size:ge10g')
    expect(parseFilterParam(filterToParam(f))).toEqual(f)
    expect(parseFilterParam('size:lt1m;cat:code')).toEqual({ cat: ['code'], age: [], size: ['lt1m'] })
    expect(filterToParam({ cat: [], age: [], size: [] })).toBe('')
    expect(parseFilterParam('')).toEqual({ cat: [], age: [], size: [] })
  })
  it.each(['cat:video;cat:audio', 'cat:', 'foo:x', 'age:lt3d', 'cat', 'cat:video,', '__proto__:x', 'constructor:x'])('rejects %j', (s) => {
    expect(parseFilterParam(s)).toBeNull()
  })
})
