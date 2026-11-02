import { t } from './i18n/en'

export type Freq = 'manual' | 'nightly' | 'weekly' | 'monthly' | 'custom'
export type Parsed = { freq: Freq; time: string; day: number; dom: number }

function toTime(min: string, hr: string): string | null {
  const mi = Number(min)
  const h = Number(hr)
  if (mi > 59 || h > 23) return null
  return `${String(h).padStart(2, '0')}:${String(mi).padStart(2, '0')}`
}

// Reads a stored cron back into the preset controls. Anything the presets cannot express is custom.
export function parseCron(cron: string): Parsed {
  const s = cron.trim()
  const fallback: Parsed = { freq: 'custom', time: '03:00', day: 0, dom: 1 }
  if (s === '') return { ...fallback, freq: 'manual' }
  const n = /^(\d{1,2}) (\d{1,2}) \* \* \*$/.exec(s)
  const w = /^(\d{1,2}) (\d{1,2}) \* \* ([0-6])$/.exec(s)
  const m = /^(\d{1,2}) (\d{1,2}) (\d{1,2}) \* \*$/.exec(s)
  const hit = n ?? w ?? m
  if (!hit) return fallback
  const time = toTime(hit[1], hit[2])
  if (time === null) return fallback
  if (n) return { ...fallback, freq: 'nightly', time }
  if (w) return { ...fallback, freq: 'weekly', time, day: Number(w[3]) }
  const dom = Number(m![3])
  if (dom < 1 || dom > 28) return fallback
  return { ...fallback, freq: 'monthly', time, dom }
}

// A short label for a stored cron, for places with little room. Custom crons are shown as written.
export function shortCron(cron: string): string {
  const p = parseCron(cron)
  if (p.freq === 'manual') return t.admin.roots.shortManual
  if (p.freq === 'nightly') return t.admin.roots.shortNightly(p.time)
  if (p.freq === 'weekly') return t.admin.roots.shortWeekly(t.admin.roots.weekdays[p.day], p.time)
  if (p.freq === 'monthly') return t.admin.roots.shortMonthly(p.dom, p.time)
  return cron.trim()
}

// A stored cron in words. Custom crons that the presets cannot describe are shown as written.
export function describeCron(cron: string): string {
  const p = parseCron(cron)
  if (p.freq === 'manual') return t.admin.roots.schedSumManual
  if (p.freq === 'nightly') return t.admin.roots.schedSumNightly(p.time)
  if (p.freq === 'weekly') return t.admin.roots.schedSumWeekly(t.admin.roots.weekdays[p.day], p.time)
  if (p.freq === 'monthly') return t.admin.roots.schedSumMonthly(p.dom, p.time)
  return cron.trim()
}
