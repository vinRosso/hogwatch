import { displayName } from './fname'
import { formatBytes } from './format'
import { t } from './i18n/en'

const KEY = 'hogwatch.listings'

/** What this tab knows about a listing it started or opened; the server keeps none of it. */
export type ListingMeta = {
  id: number
  /** Escaped root-relative path of the folder (or its name for a root's top), for the display name. */
  where?: string
  mode?: 'children' | 'subtree'
  exts?: string[]
  minSize?: number
  minAgeDays?: number
  /** Unix seconds when this tab first saw it. */
  at?: number
}

const isMode = (v: unknown): v is 'children' | 'subtree' => v === 'children' || v === 'subtree'

function clean(v: unknown): ListingMeta | null {
  // Older tabs stored bare ids.
  if (typeof v === 'number') return Number.isInteger(v) ? { id: v } : null
  if (typeof v !== 'object' || v === null) return null
  const o = v as Record<string, unknown>
  if (typeof o.id !== 'number' || !Number.isInteger(o.id)) return null
  const m: ListingMeta = { id: o.id }
  if (typeof o.where === 'string') m.where = o.where
  if (isMode(o.mode)) m.mode = o.mode
  if (Array.isArray(o.exts)) m.exts = o.exts.filter((e): e is string => typeof e === 'string')
  if (typeof o.minSize === 'number') m.minSize = o.minSize
  if (typeof o.minAgeDays === 'number') m.minAgeDays = o.minAgeDays
  if (typeof o.at === 'number') m.at = o.at
  return m
}

/** The contract has no list endpoint, so this tab remembers the listings it started (newest first). */
export function recentListings(): ListingMeta[] {
  try {
    const v = JSON.parse(sessionStorage.getItem(KEY) ?? '[]')
    return Array.isArray(v) ? v.map(clean).filter((m): m is ListingMeta => m !== null).slice(0, 20) : []
  } catch {
    return []
  }
}

export function listingMeta(id: number): ListingMeta | null {
  return recentListings().find((m) => m.id === id) ?? null
}

function save(list: ListingMeta[]) {
  try {
    sessionStorage.setItem(KEY, JSON.stringify(list.slice(0, 20)))
  } catch {
    /* storage unavailable: the listing page still works by its link */
  }
}

/** Puts a listing first; what it already had is kept for any field the new call leaves out. */
export function rememberListing(id: number, meta: Omit<ListingMeta, 'id'> = {}) {
  const old = listingMeta(id)
  const next: ListingMeta = { ...old, ...meta, id, at: old?.at ?? meta.at ?? Math.floor(Date.now() / 1000) }
  save([next, ...recentListings().filter((m) => m.id !== id)])
}

export function forgetListing(id: number) {
  save(recentListings().filter((m) => m.id !== id))
}

export function clearListings() {
  save([])
}

/** The folder the listing is of, else "Listing N" for one this tab only knows by its link. */
export function listingTitle(m: ListingMeta): string {
  return m.where ? displayName(m.where) : t.listing.open(m.id)
}

/** The mode and filters in a few words, e.g. "Everything below · mp4, mkv · over 10 GB". */
export function listingSummary(m: ListingMeta): string {
  const parts: string[] = []
  if (m.mode) parts.push(t.listing.modeShort[m.mode])
  if (m.exts?.length) parts.push(m.exts.join(', '))
  if (m.minSize) parts.push(t.listing.overSize(formatBytes(m.minSize)))
  if (m.minAgeDays) parts.push(t.listing.olderThan(m.minAgeDays))
  return parts.join(' · ')
}
