/**
 * The keys whose value changed, for a PATCH body. A key set to null stays in the result
 * (null clears the field; an absent key leaves it alone). `password` is sent only when non-empty.
 */
export function diffPatch<T extends object>(before: T, after: T, keys: (keyof T)[]): Partial<T> {
  const out: Partial<T> = {}
  for (const k of keys) {
    if (k === 'password') {
      const v = after[k] as unknown
      if (typeof v === 'string' && v !== '') out[k] = after[k]
      continue
    }
    if (before[k] !== after[k]) out[k] = after[k]
  }
  return out
}

export type RelPathError = 'nul' | 'empty_segment' | 'dot_segment'

/** Client-side check of a typed relative path: no NUL, no empty segment, no `.` or `..`. The server checks again. */
export function checkRelPath(p: string): RelPathError | null {
  if (p.includes('\0')) return 'nul'
  if (p === '') return null
  for (const seg of p.split('/')) {
    if (seg === '') return 'empty_segment'
    if (seg === '.' || seg === '..') return 'dot_segment'
  }
  return null
}
