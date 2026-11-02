const KEY = 'hogwatch.listings'

/** The contract has no list endpoint, so this tab remembers the ids it started (newest first). */
export function recentListings(): number[] {
  try {
    const v = JSON.parse(sessionStorage.getItem(KEY) ?? '[]')
    return Array.isArray(v) ? v.filter((n): n is number => Number.isInteger(n)).slice(0, 20) : []
  } catch {
    return []
  }
}

export function rememberListing(id: number) {
  try {
    sessionStorage.setItem(KEY, JSON.stringify([id, ...recentListings().filter((x) => x !== id)].slice(0, 20)))
  } catch {
    /* storage unavailable: the listing page still works by its link */
  }
}
