import { api, ApiError } from './api/client'
import type { Anchor, Node, RootInfo } from './api/types'
import { displayName } from './fname'
import { emptyFilter, filterToParam, parseFilterParam, type Filter } from './filter'
import { t } from './i18n/en'
import { navigate, router, setQuery } from './router.svelte'
import { session } from './session.svelte'
import { toast } from './toast.svelte'


type Status = 'idle' | 'loading' | 'empty' | 'error'
export type Crumb = { label: string; rel: string }

// Where each ref seen so far lives (a node and its parent), so a stale ref (a new scan
// renumbers every node) is found again by its own root-relative path, never another node's.
const known = new Map<string, { root_id: number; rel_path: string }>()

const keepQuery = () => {
  const q = new URLSearchParams()
  for (const k of ['filter', 'depth', 'tab']) if (router.query[k]) q.set(k, router.query[k])
  const s = q.toString()
  return s ? `?${s}` : ''
}

function setEmpty(reason: string) {
  browse.status = 'empty'
  browse.emptyReason = reason
  browse.node = null
}

// /srv is the host mount point the scanner sees; it says nothing to the user, so the label drops it.
const hostLabel = (host: string) => displayName(host.replace(/^\/srv(?=\/)/, ''))

/** Breadcrumbs from the root-relative path; the anchor's host path labels the first one. */
export function crumbs(relPath: string, anchors: Anchor[], rootName: string): Crumb[] {
  const segs = relPath === '' ? [] : relPath.split('/')
  let best = -1
  let label = displayName(rootName)
  for (const a of anchors) {
    const host = a.host_path
    for (let n = segs.length; n >= 0; n--) {
      const suffix = n === 0 ? '' : '/' + segs.slice(0, n).join('/')
      if (n > best && (n === 0 || host.endsWith(suffix))) {
        best = n
        label = hostLabel(host)
        break
      }
    }
  }
  const from = Math.max(best, 0)
  const out: Crumb[] = [{ label, rel: segs.slice(0, from).join('/') }]
  for (let i = from; i < segs.length; i++) out.push({ label: displayName(segs[i]), rel: segs.slice(0, i + 1).join('/') })
  return out
}

let loadSeq = 0

/** Treemap levels the toolbar offers; the API allows up to 6. */
export const DEPTHS = [1, 2, 3, 4, 5] as const
export type Depth = (typeof DEPTHS)[number]

export const browse = $state({
  root: null as RootInfo | null,
  node: null as Node | null,
  filter: emptyFilter() as Filter,
  depth: 2 as Depth,
  status: 'idle' as Status,
  emptyReason: null as string | null,
  notice: null as string | null,

  open(ref: string) {
    browse.notice = null
    navigate(`/browse/${ref}${keepQuery()}`)
  },

  up() {
    const p = browse.node?.parent_ref
    if (p) browse.open(p)
  },

  setFilter(f: Filter) {
    setQuery({ filter: filterToParam(f) })
  },

  setDepth(d: Depth) {
    // The account keeps the level; the URL only carries one that a shared link names.
    browse.depth = d
    session.setPrefs({ depth: d })
    setQuery({ depth: undefined })
  },

  /** Reads filter and depth from the URL query, else the account's depth. An invalid filter is dropped, with a toast and no request. */
  applyQuery(q: Record<string, string>) {
    const f = parseFilterParam(q.filter ?? '')
    if (f === null) {
      toast(t.browse.badFilter, 'error')
      browse.filter = emptyFilter()
      setQuery({ filter: undefined }, { replace: true })
    } else browse.filter = f
    browse.depth = DEPTHS.find((d) => String(d) === q.depth) ?? DEPTHS.find((d) => d === session.prefs().depth) ?? 2
  },

  /** Picks the start state for a ref (or none): empty states with a reason, or loads the node. */
  async start(ref: string | null, retried = false) {
    // The roots may have changed since sign-in (a first scan, a new path, a newer scan renumbering anchors).
    if (ref === null || retried || !session.roots.find((r) => r.id === session.rootId)?.anchors.length || session.roots.find((r) => r.id === session.rootId)?.last_scanned_at === null) {
      await session.refreshRoots()
    }
    const root = session.roots.find((r) => r.id === session.rootId) ?? null
    browse.root = root
    if (root === null || root.anchors.length === 0) return setEmpty(t.browse.noPaths)
    if (root.last_scanned_at === null) return setEmpty(t.browse.neverScanned)
    if (ref === null) {
      const a = root.anchors.find((x) => x.ref !== null)
      if (!a) return setEmpty(t.browse.notInScan(displayName(root.anchors[0].host_path)))
      navigate(`/browse/${a.ref}${keepQuery()}`, { replace: true })
      ref = a.ref
    }
    await browse.loadNode(ref!, retried)
  },

  /** The single place that turns a 404 into one /resolve call; it never loops. */
  async loadNode(ref: string, retried = false) {
    const mine = ++loadSeq
    browse.status = 'loading'
    try {
      const n = await api.get<Node>(`/nodes/${ref}`)
      if (mine !== loadSeq) return
      browse.node = n
      known.set(n.ref, { root_id: n.root_id, rel_path: n.rel_path })
      if (n.parent_ref) known.set(n.parent_ref, { root_id: n.root_id, rel_path: n.rel_path.split('/').slice(0, -1).join('/') })
      if (session.rootId !== n.root_id) session.rootId = n.root_id
      browse.root = session.roots.find((r) => r.id === n.root_id) ?? browse.root
      browse.status = 'idle'
      browse.emptyReason = null
    } catch (e) {
      if (mine !== loadSeq) return
      if (!(e instanceof ApiError) || e.status !== 404 || retried) {
        browse.status = 'error'
        browse.emptyReason = t.browse.loadFailed
        return
      }
      const where = known.get(ref)
      if (where) {
        try {
          const r = await api.get<{ ref: string }>('/resolve', { root: where.root_id, path: where.rel_path })
          if (mine !== loadSeq) return
          navigate(`/browse/${r.ref}${keepQuery()}`, { replace: true })
          await browse.loadNode(r.ref, true)
          return
        } catch {
          /* fall through to the start folder */
        }
      }
      browse.notice = t.browse.moved
      await browse.start(null, true)
    }
  },
})
