import { describe, expect, it } from 'vitest'
import { sourceURLFor } from './buildinfo'

describe('sourceURLFor', () => {
  it('links the exact commit', () => {
    expect(sourceURLFor('a1b2c3d')).toBe('https://github.com/vinRosso/hogwatch/tree/a1b2c3d')
  })
  it('falls back to the repo for an empty or odd commit', () => {
    expect(sourceURLFor('')).toBe('https://github.com/vinRosso/hogwatch')
    expect(sourceURLFor('../x')).toBe('https://github.com/vinRosso/hogwatch')
  })
})
