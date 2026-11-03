import { api } from './api/client'
import type { Node, RootInfo } from './api/types'

/**
 * The absolute host path of a root-relative path. The longest anchor that contains it supplies the
 * host path for its own part; the rest is appended. Null when no anchor contains the path, which
 * means it is outside what the caller can see.
 */
export async function hostPathOf(root: RootInfo, rel: string): Promise<string | null> {
  let best: { host: string; rel: string } | null = null
  for (const a of root.anchors) {
    if (!a.found || !a.ref) continue
    const anchorRel = (await api.get<Node>(`/nodes/${a.ref}`)).rel_path
    const inside = anchorRel === '' || rel === anchorRel || rel.startsWith(`${anchorRel}/`)
    if (inside && (best === null || anchorRel.length > best.rel.length)) best = { host: a.host_path, rel: anchorRel }
  }
  if (best === null) return null
  const rest = rel.slice(best.rel.length).replace(/^\//, '')
  return rest === '' ? best.host : `${best.host}/${rest}`
}
