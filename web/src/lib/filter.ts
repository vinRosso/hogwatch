export const categories = ['video', 'audio', 'image', 'archive', 'disk-image', 'document', 'code', 'other'] as const
export const ages = ['lt1w', 'lt1mo', 'lt3mo', 'lt1y', 'lt2y', 'lt5y', 'lt10y', 'ge10y'] as const
export const sizes = ['lt1m', 'lt10m', 'lt100m', 'lt1g', 'lt10g', 'ge10g'] as const

export type CategoryName = (typeof categories)[number]
export type AgeBucket = (typeof ages)[number]
export type SizeBucket = (typeof sizes)[number]
export type Filter = { cat: CategoryName[]; age: AgeBucket[]; size: SizeBucket[] }

export const emptyFilter = (): Filter => ({ cat: [], age: [], size: [] })

export function isEmpty(f: Filter): boolean {
  return !f.cat.length && !f.age.length && !f.size.length
}

/** Contiguous runs of bucket indices (inclusive) among `toks`, in bucket order; unknown tokens are ignored. */
export function bucketRuns(vocab: readonly string[], toks: readonly string[]): [number, number][] {
  const idx = [...new Set(toks.map((t) => vocab.indexOf(t)))].filter((i) => i >= 0).sort((a, b) => a - b)
  const runs: [number, number][] = []
  for (const i of idx) {
    const last = runs.at(-1)
    if (last && last[1] === i - 1) last[1] = i
    else runs.push([i, i])
  }
  return runs
}

/** Filter grammar: keys in the order cat, age, size; empty lists omitted. */
export function filterToParam(f: Filter): string {
  const parts: string[] = []
  if (f.cat.length) parts.push(`cat:${f.cat.join(',')}`)
  if (f.age.length) parts.push(`age:${f.age.join(',')}`)
  if (f.size.length) parts.push(`size:${f.size.join(',')}`)
  return parts.join(';')
}

/** Null on any grammar error: unknown key or token, duplicate key, empty list. */
export function parseFilterParam(s: string): Filter | null {
  const f = emptyFilter()
  if (s === '') return f
  const seen = new Set<string>()
  const vocab: Record<string, readonly string[]> = { cat: categories, age: ages, size: sizes }
  for (const part of s.split(';')) {
    const i = part.indexOf(':')
    if (i < 0) return null
    const key = part.slice(0, i)
    const list = part.slice(i + 1)
    if (!Object.hasOwn(vocab, key) || seen.has(key) || list === '') return null
    seen.add(key)
    const toks = list.split(',')
    for (const tok of toks) if (!vocab[key].includes(tok)) return null
    ;(f as Record<string, string[]>)[key] = toks
  }
  return f
}
