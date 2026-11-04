export type PathSort = 'size' | 'name'

/** A part's fraction of the whole, clamped to 0…1; zero when there is no whole. */
export function share(bytes: number, total: number): number {
  return total > 0 ? Math.min(Math.max(bytes / total, 0), 1) : 0
}

type Sortable = { found: boolean; redundant: boolean; usage: { bytes_alloc: number } }

/** Healthy paths first by the chosen key; the ones that are missing or redundant go last, in the same order. */
export function sortPaths<T extends Sortable>(rows: T[], by: PathSort, name: (p: T) => string): T[] {
  const flagged = (p: T) => (!p.found || p.redundant ? 1 : 0)
  const key = (a: T, b: T) => (by === 'size' ? b.usage.bytes_alloc - a.usage.bytes_alloc : 0) || name(a).localeCompare(name(b))
  return [...rows].sort((a, b) => flagged(a) - flagged(b) || key(a, b))
}
