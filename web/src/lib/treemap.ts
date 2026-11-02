import { hierarchy, treemap, treemapSquarify } from 'd3-hierarchy'
import type { TreemapNode } from './api/types'
import { displayName } from './fname'
import { formatBytes, formatCount } from './format'
import { t } from './i18n/en'

export type Rect = {
  x: number
  y: number
  w: number
  h: number
  depth: number
  node: TreemapNode
  parent: TreemapNode | null
  /** The user owning the block's folder; null when no user path covers it. */
  owner: number | null
  /** Sibling index inside the parent, used to lighten nested levels. */
  sib: number
  /** The block's full name, for the hover tooltip; `label` is cut to fit and dropped on small blocks. */
  name: string
  label: string | null
  /** File count and size, drawn right-aligned in the block. */
  meta: string | null
}

const PAD_TOP = 18
const PAD_INNER = 3
/** Crowded small parents get a thinner gap between their children, so the blocks keep their size. */
const PAD_INNER_MIN = 0
/** Rects thinner than this are dropped: drawn, they come out as stray lines. */
const MIN_SIDE = 2

/** Big enough for a name and meta line. */
const labelled = (w: number, h: number) => w > 46 && h > 30

/** File count and size, as shown at the right of a block: "999 files | 9.99 GB". */
function blockMeta(n: TreemapNode): string {
  return `${t.treemap.fileCount(formatCount(n.files), n.files)} | ${formatBytes(n.bytes_alloc)}`
}

/** A folder's name, ".ext" for an extension block, a generic name for the other kinds. */
function blockName(n: TreemapNode): string {
  if (n.kind === 'ext') return n.name ? `.${displayName(n.name)}` : t.ext.none
  if (n.kind === 'files') return t.treemap.otherFiles
  if (n.kind === 'rest') return t.list.rest_kind
  return displayName(n.name)
}

/**
 * Squarified layout of the server's tree into a w x h box. Depth-0 (the current
 * node) is not drawn; its children are the first level. A container (a node
 * above `depth` with children) keeps a label strip on top.
 *
 * `ownerOf` gets the names of a folder's path below the current node (empty for the
 * current node itself) and returns the user who owns it. Extension and other-file
 * blocks belong to the folder they sit in; the rest block belongs to nobody.
 */
export function layoutTreemap(
  root: TreemapNode,
  w: number,
  h: number,
  depth: number,
  ownerOf: (names: string[]) => number | null = () => null,
): Rect[] {
  const h0 = hierarchy<TreemapNode>(root, (n) => (n.children.length ? n.children : null)).sum((n) =>
    n.children.length ? 0 : n.bytes_alloc,
  )
  const laid = treemap<TreemapNode>()
    .size([w, h])
    .tile(treemapSquarify)
    // d3 sets a node's box before asking for its padding. A strip on a box too small
    // to label would leave the children no room, and d3 then puts them below the box.
    .paddingTop((d) => (d.depth > 0 && d.depth < depth && d.children && labelled(d.x1 - d.x0, d.y1 - d.y0) ? PAD_TOP : 0))
    .paddingInner((d) => Math.min(PAD_INNER, Math.max(PAD_INNER_MIN, Math.min(d.x1 - d.x0, d.y1 - d.y0) / 40)))(h0)

  const out: Rect[] = []
  const ownerAt = new Map<TreemapNode, number | null>()
  laid.each((d) => {
    if (d.depth === 0) return
    const rw = d.x1 - d.x0
    const rh = d.y1 - d.y0
    if (rw < MIN_SIDE || rh < MIN_SIDE) return
    const n = d.data
    const p = d.parent!
    const sib = p.children!.indexOf(d)
    let owner: number | null
    if (n.kind === 'rest') owner = null
    else if (n.kind === 'dir') owner = ownerOf(d.ancestors().filter((a) => a.depth > 0).reverse().map((a) => a.data.name))
    else owner = p.depth === 0 ? ownerOf([]) : (ownerAt.get(p.data) ?? null)
    ownerAt.set(n, owner)
    out.push({
      x: d.x0,
      y: d.y0,
      w: rw,
      h: rh,
      depth: d.depth,
      node: n,
      parent: p.depth === 0 ? root : p.data,
      owner,
      sib,
      name: blockName(n),
      label: labelled(rw, rh) ? blockName(n) : null,
      meta: labelled(rw, rh) ? blockMeta(n) : null,
    })
  })
  return out
}

/** The deepest rect containing the point, or null. */
export function hitTest(rects: Rect[], x: number, y: number): Rect | null {
  let best: Rect | null = null
  for (const r of rects) {
    if (x >= r.x && x < r.x + r.w && y >= r.y && y < r.y + r.h && (best === null || r.depth >= best.depth)) best = r
  }
  return best
}

/** Nearest rect from `from` in a direction, by centre distance among those lying that way. */
export function neighbour(rects: Rect[], from: Rect, dir: 'left' | 'right' | 'up' | 'down'): Rect | null {
  const cx = from.x + from.w / 2
  const cy = from.y + from.h / 2
  let best: Rect | null = null
  let bestD = Infinity
  for (const r of rects) {
    if (r === from || r.node.children.length) continue
    const dx = r.x + r.w / 2 - cx
    const dy = r.y + r.h / 2 - cy
    const ok = dir === 'right' ? dx > 0 : dir === 'left' ? dx < 0 : dir === 'down' ? dy > 0 : dy < 0
    if (!ok) continue
    const along = dir === 'left' || dir === 'right' ? Math.abs(dx) : Math.abs(dy)
    const across = dir === 'left' || dir === 'right' ? Math.abs(dy) : Math.abs(dx)
    const d = along + across * 2
    if (d < bestD) {
      bestD = d
      best = r
    }
  }
  return best
}
