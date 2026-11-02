export const router = $state({
  path: location.pathname,
  query: Object.fromEntries(new URLSearchParams(location.search)) as Record<string, string>,
})

function sync() {
  router.path = location.pathname
  router.query = Object.fromEntries(new URLSearchParams(location.search))
}

export function navigate(to: string, opts: { replace?: boolean } = {}) {
  const u = new URL(to, location.origin)
  if (u.origin !== location.origin) {
    location.assign(to)
    return
  }
  const target = u.pathname + u.search + u.hash
  if (opts.replace) history.replaceState(null, '', target)
  else history.pushState(null, '', target)
  sync()
}

/** Replaces only the query string (for tab, window and filter state kept in the URL). */
export function setQuery(patch: Record<string, string | undefined>, opts: { replace?: boolean } = {}) {
  const q = new URLSearchParams(location.search)
  for (const [k, v] of Object.entries(patch)) {
    if (v === undefined || v === '') q.delete(k)
    else q.set(k, v)
  }
  const s = q.toString()
  navigate(location.pathname + (s ? `?${s}` : ''), opts)
}

/** Matches `/a/:id/b` against a path; a trailing `/*` matches any rest (captured as `rest`). */
export function match(pattern: string, path: string): Record<string, string> | null {
  const pp = pattern.split('/').filter(Boolean)
  const ap = path.split('/').filter(Boolean)
  const wild = pp[pp.length - 1] === '*'
  if (wild) pp.pop()
  if (wild ? ap.length < pp.length : ap.length !== pp.length) return null
  const out: Record<string, string> = {}
  for (let i = 0; i < pp.length; i++) {
    if (pp[i].startsWith(':')) {
      try {
        out[pp[i].slice(1)] = decodeURIComponent(ap[i])
      } catch {
        return null
      }
    } else if (pp[i] !== ap[i]) return null
  }
  if (wild) out.rest = ap.slice(pp.length).join('/')
  return out
}

/** Only same-site absolute paths survive; everything else becomes "/" (no open redirect). */
export function safeNext(next: string | null): string {
  if (!next) return '/'
  // Browsers drop tab, CR and LF while parsing a URL, so "/\t/evil.com" means "//evil.com".
  const flat = next.replace(/[\t\r\n]/g, '')
  if (flat !== next || !next.startsWith('/') || next.startsWith('//') || next.startsWith('/\\')) return '/'
  return next
}

/** Intercepts plain same-origin link clicks so navigation stays in the SPA. */
export function installLinkInterception(): () => void {
  const onClick = (e: MouseEvent) => {
    if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return
    const a = (e.target as Element | null)?.closest?.('a[href]') as HTMLAnchorElement | null
    if (!a || (a.target && a.target !== '_self') || a.hasAttribute('download')) return
    const u = new URL(a.href, location.origin)
    if (u.origin !== location.origin || u.pathname.startsWith('/api/')) return
    e.preventDefault()
    navigate(u.pathname + u.search + u.hash)
  }
  const onPop = () => sync()
  document.addEventListener('click', onClick)
  window.addEventListener('popstate', onPop)
  return () => {
    document.removeEventListener('click', onClick)
    window.removeEventListener('popstate', onPop)
  }
}

/**
 * A full page load of a same-site path. Signing in uses it so that nothing the previous
 * account loaded (the browsed folder, owners, the selected root) outlives the sign-in.
 */
export function hardNavigate(to: string) {
  location.assign(to)
}
