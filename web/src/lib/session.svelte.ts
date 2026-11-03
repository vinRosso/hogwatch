import { api, ApiError, setUnauthorizedHandler } from './api/client'
import type { AuthConfig, Me, MyUsage, Prefs, RootInfo } from './api/types'
import { t } from './i18n/en'
import { navigate, router } from './router.svelte'

export type QuotaState = { used: number; limit: number; pct: number; level: 'ok' | 'warn80' | 'warn95' }

export const session = $state({
  me: null as Me | null,
  usage: null as MyUsage | null,
  roots: [] as RootInfo[],
  authConfig: null as AuthConfig | null,
  rootId: null as number | null,
  loaded: false,
  loadError: null as string | null,

  /** Loads the caller, their usage (growth over 30 d) and roots. A 401 leaves `me` null; any other failure sets `loadError`. */
  async load() {
    session.loadError = null
    try {
      const [me, usage, roots] = await Promise.all([
        api.get<Me>('/me'),
        api.get<MyUsage>('/me/usage', { window: '30d' }),
        api.get<{ items: RootInfo[] }>('/roots'),
      ])
      session.me = me
      session.usage = usage
      session.roots = roots.items
      if (session.rootId === null || !roots.items.some((r) => r.id === session.rootId)) {
        // The root the account last picked, if it is still visible to them.
        const saved = roots.items.find((r) => r.id === me.prefs?.root_id)
        session.rootId = saved?.id ?? roots.items[0]?.id ?? null
      }
    } catch (e) {
      session.me = null
      if (!(e instanceof ApiError && e.status === 401)) session.loadError = t.common.error
    } finally {
      session.loaded = true
    }
  },

  /** Re-reads the roots (anchors and scan state); keeps the old list when the request fails. */
  async refreshRoots() {
    try {
      const roots = await api.get<{ items: RootInfo[] }>('/roots')
      session.roots = roots.items
      if (session.rootId === null || !roots.items.some((r) => r.id === session.rootId)) session.rootId = roots.items[0]?.id ?? null
    } catch {
      /* keep what we have */
    }
  },

  async loadAuthConfig() {
    session.authConfig = await api.get<AuthConfig>('/auth/config')
  },

  async logout() {
    try {
      await api.post('/auth/logout')
    } finally {
      session.me = null
      session.usage = null
      session.roots = []
      navigate('/login')
    }
  },

  /** The account's display preferences; a key the account never set is absent, so callers bring the default. */
  prefs(): Prefs {
    return session.me?.prefs ?? {}
  },

  /** Saves display preferences to the account. They apply at once; a failed save is dropped and the next load shows what the server holds. */
  setPrefs(patch: Prefs) {
    if (!session.me) return
    session.me.prefs = { ...session.me.prefs, ...patch }
    void api.patch('/me/prefs', patch).catch(() => {})
  },

  /** Listing files needs `can_list_files`, which admins have implicitly. */
  canListFiles(): boolean {
    return !!session.me && (session.me.can_list_files || session.me.is_admin)
  },

  /** The quota is measured on allocated bytes; null when no quota is set. */
  quota(): QuotaState | null {
    const u = session.usage
    if (!u || u.quota_bytes === null) return null
    const used = u.total.bytes_alloc
    const limit = u.quota_bytes
    const pct = limit > 0 ? Math.floor((used / limit) * 100) : 0
    return { used, limit, pct, level: pct >= 95 ? 'warn95' : pct >= 80 ? 'warn80' : 'ok' }
  },
})

/** Sends the browser to /login?next=<current page> on any 401 (never from the auth pages). */
export function installAuthRedirect() {
  setUnauthorizedHandler(() => {
    if (router.path === '/login' || router.path === '/setup') return
    session.me = null
    const here = location.pathname + location.search
    navigate(`/login?next=${encodeURIComponent(here)}`, { replace: true })
  })
}
