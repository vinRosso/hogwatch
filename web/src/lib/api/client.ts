export class ApiError extends Error {
  status: number
  title: string
  detail: string
  retryAfter: number | null
  constructor(status: number, title: string, detail: string, retryAfter: number | null) {
    super(detail || title)
    this.name = 'ApiError'
    this.status = status
    this.title = title
    this.detail = detail
    this.retryAfter = retryAfter
  }
}

type Query = Record<string, string | number | undefined>

const base = '/api/v1'
let onUnauthorized: () => void = () => {}

/** The session store sets this; it runs on every 401 before the error is thrown. */
export function setUnauthorizedHandler(fn: () => void) {
  onUnauthorized = fn
}

function url(path: string, query?: Query): string {
  const q = new URLSearchParams()
  for (const [k, v] of Object.entries(query ?? {})) if (v !== undefined) q.set(k, String(v))
  const s = q.toString()
  return base + path + (s ? `?${s}` : '')
}

async function request<T>(method: string, path: string, query?: Query, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {}
  if (method !== 'GET') headers['X-Hogwatch-CSRF'] = '1'
  let payload: string | undefined
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json'
    payload = JSON.stringify(body)
  }
  const res = await fetch(url(path, query), { method, headers, body: payload, credentials: 'same-origin' })
  if (res.status === 401) onUnauthorized()
  if (!res.ok) {
    let title = res.statusText
    let detail = ''
    try {
      const p = await res.json()
      title = p.title ?? title
      detail = p.detail ?? ''
    } catch {
      /* not a problem document */
    }
    const ra = res.headers.get('Retry-After')
    const secs = ra !== null && /^\d+$/.test(ra) ? parseInt(ra, 10) : null
    throw new ApiError(res.status, title, detail, secs)
  }
  if (res.status === 204) return undefined as T
  const text = await res.text()
  return (text ? JSON.parse(text) : undefined) as T
}

export const api = {
  get: <T>(path: string, query?: Query) => request<T>('GET', path, query),
  post: <T>(path: string, body?: unknown) => request<T>('POST', path, undefined, body),
  put: <T>(path: string, body?: unknown) => request<T>('PUT', path, undefined, body),
  patch: <T>(path: string, body?: unknown) => request<T>('PATCH', path, undefined, body),
  del: (path: string) => request<void>('DELETE', path),
}
