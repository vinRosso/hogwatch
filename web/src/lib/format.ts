const units = ['B', 'kB', 'MB', 'GB', 'TB', 'PB', 'EB']

/** SI bytes (1 kB = 1000 B) with 3 significant digits, as the spec's bucket names do. */
export function formatBytes(n: number): string {
  if (!Number.isFinite(n)) return '—'
  const sign = n < 0 ? '-' : ''
  let v = Math.abs(n)
  if (v < 999.5) return `${sign}${Math.round(v)} B`
  let i = 0
  while (v >= 1000 && i < units.length - 1) {
    v /= 1000
    i++
  }
  let s = v >= 100 ? v.toFixed(0) : v >= 10 ? v.toFixed(1) : v.toFixed(2)
  if (parseFloat(s) >= 1000 && i < units.length - 1) {
    i++
    s = (parseFloat(s) / 1000).toFixed(2)
  }
  return `${sign}${s} ${units[i]}`
}

/** Parses "10 GB", "1.5kB", "2048" (bytes). Null when it is not a size. */
export function parseBytes(s: string): number | null {
  const m = /^\s*(\d+(?:\.\d+)?)\s*([kKMGTPE]?)[bB]?\s*$/.exec(s)
  if (!m) return null
  const idx = ['', 'K', 'M', 'G', 'T', 'P', 'E'].indexOf(m[2].toUpperCase())
  return Math.round(parseFloat(m[1]) * 1000 ** idx)
}

export function formatCount(n: number): string {
  return Math.round(n).toLocaleString('en-US')
}

/** Calendar date (UTC) as YYYY-MM-DD. */
export function formatDate(unixSec: number): string {
  return new Date(unixSec * 1000).toISOString().slice(0, 10)
}

/** UTC date and minute as YYYY-MM-DD HH:MM. */
export function formatDateTime(unixSec: number): string {
  return new Date(unixSec * 1000).toISOString().slice(0, 16).replace('T', ' ')
}

/** Elapsed seconds as "42 s", "3 min 05 s" or "2 h 07 min". */
export function formatDuration(sec: number): string {
  const s = Math.max(0, Math.round(sec))
  if (s < 60) return `${s} s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m} min ${String(s % 60).padStart(2, '0')} s`
  return `${Math.floor(m / 60)} h ${String(m % 60).padStart(2, '0')} min`
}

export function formatAge(unixSec: number, now: number): string {
  const d = Math.max(0, now - unixSec)
  if (d < 60) return 'just now'
  if (d < 3600) return `${Math.floor(d / 60)} min ago`
  if (d < 86400) return `${Math.floor(d / 3600)} h ago`
  if (d < 60 * 86400) return `${Math.floor(d / 86400)} d ago`
  return formatDate(unixSec)
}
