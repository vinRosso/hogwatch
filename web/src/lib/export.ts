import { ApiError } from './api/client'

export type ExportFormat = 'csv' | 'json'
export type ExportWhat = 'tree' | 'top' | 'usage'

/**
 * Starts a download. A check request first learns whether the server would accept the export
 * (the concurrent-export limit answers 429); the check does none of the export's work and gives its slot back before it answers, so it
 * cannot crowd out the download that follows. On success the browser fetches the export itself
 * through a link, so Content-Disposition saves the file.
 */
export async function startExport(ref: string, format: ExportFormat, what: ExportWhat): Promise<'started' | { retryAfter: number }> {
  const url = `/api/v1/export/${encodeURIComponent(ref)}?format=${format}&what=${what}`
  const res = await fetch(`${url}&check=true`, { credentials: 'same-origin' })
  if (res.status === 204) {
    // A download link, not a page navigation: if the second request is refused the page stays.
    const a = document.createElement('a')
    a.href = url
    a.download = ''
    a.click()
    return 'started'
  }
  if (res.status === 429) {
    const ra = res.headers.get('Retry-After')
    return { retryAfter: ra !== null && /^\d+$/.test(ra) ? parseInt(ra, 10) : 30 }
  }
  let title = res.statusText
  let detail = ''
  try {
    const p = await res.json()
    title = p.title ?? title
    detail = p.detail ?? ''
  } catch {
    /* not a problem document */
  }
  throw new ApiError(res.status, title, detail, null)
}
