import { describe, expect, it } from 'vitest'
import { displayName } from './fname'

describe('displayName', () => {
  it.each([
    ['a\nb', 'a␊b'],
    ['\t', '␉'],
    ['x‮y', 'x\\u{202E}y'],
    ['a⁦b⁩', 'a\\u{2066}b\\u{2069}'],
    ['\u0085', '\\u{85}'],
    ['\u007f', '␡'],
    ['\\xff', '\\xff'],
    ['<b>', '<b>'],
    ['=cmd', '=cmd'],
    ['plain ünï', 'plain ünï'],
  ])('%j -> %j', (a, b) => expect(displayName(a)).toBe(b))
})
