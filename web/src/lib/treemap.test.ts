import { describe, expect, it } from 'vitest'
import type { TreemapNode } from './api/types'
import { hitTest, layoutTreemap, neighbour } from './treemap'

const leaf = (name: string, bytes: number, kind: TreemapNode['kind'] = 'dir'): TreemapNode => ({
  name,
  kind,
  ref: kind === 'dir' ? '1.1' : null,
  bytes_alloc: bytes,
  files: 1,
  children: [],
})
const dir = (name: string, kids: TreemapNode[]): TreemapNode => ({
  name,
  kind: 'dir',
  ref: '1.2',
  bytes_alloc: kids.reduce((a, k) => a + k.bytes_alloc, 0),
  files: kids.reduce((a, k) => a + k.files, 0),
  children: kids,
})
const root = (kids: TreemapNode[]) => dir('root', kids)
const area = (r: { w: number; h: number }) => r.w * r.h
const isLeaf = (r: { node: TreemapNode }) => r.node.children.length === 0

describe('layoutTreemap', () => {
  const tree = root([leaf('a', 600), leaf('b', 300), leaf('c', 150), leaf('d', 90), leaf('e', 40), leaf('f', 20)])

  it('leaf areas fill the box, less the gaps between them', () => {
    const rects = layoutTreemap(tree, 1000, 1000, 1)
    const sum = rects.filter(isLeaf).reduce((a, r) => a + area(r), 0)
    expect(sum).toBeLessThanOrEqual(1000 * 1000)
    expect(sum).toBeGreaterThan(1000 * 1000 * 0.95)
  })

  it('no two leaves overlap', () => {
    const kids = Array.from({ length: 40 }, (_, i) => leaf(`n${i}`, 1000 - i * 20))
    const rects = layoutTreemap(root(kids), 800, 500, 1).filter(isLeaf)
    for (let i = 0; i < rects.length; i++)
      for (let j = i + 1; j < rects.length; j++) {
        const a = rects[i]
        const b = rects[j]
        const overlap = a.x < b.x + b.w - 1e-6 && b.x < a.x + a.w - 1e-6 && a.y < b.y + b.h - 1e-6 && b.y < a.y + a.h - 1e-6
        expect(overlap).toBe(false)
      }
  })

  it('lays out 2000 nodes quickly', () => {
    const kids = Array.from({ length: 2000 }, (_, i) => leaf(`n${i}`, (i % 97) + 1))
    const t0 = performance.now()
    const rects = layoutTreemap(root(kids), 1200, 800, 1)
    expect(performance.now() - t0).toBeLessThan(50)
    // Slivers under 2 px are dropped; the bulk is still laid out.
    expect(rects.length).toBeGreaterThan(1800)
  })

  it('gives the rest block no owner; folders take their owner, extension and other-file blocks their folder owner', () => {
    const rects = layoutTreemap(
      root([leaf('a', 500), leaf('mp4', 400, 'ext'), leaf('', 300, 'files'), leaf('(rest)', 200, 'rest')]),
      600,
      400,
      1,
      (names) => (names.join('/') === 'a' ? 3 : null),
    )
    const by = (k: TreemapNode['kind']) => rects.find((r) => r.node.kind === k)!
    expect(by('rest').owner).toBeNull()
    expect(by('dir').owner).toBe(3)
    expect(by('ext').owner).toBeNull()
    expect(by('files').owner).toBeNull()
  })

  it('gives extension and other-file blocks of the current node the owner of that node', () => {
    const rects = layoutTreemap(root([leaf('mp4', 500, 'ext'), leaf('', 300, 'files')]), 600, 400, 1, (names) => (names.length === 0 ? 5 : null))
    expect(rects.map((r) => r.owner)).toEqual([5, 5])
  })

  it('labels extension blocks .ext, (no extension) or other files', () => {
    const rects = layoutTreemap(root([leaf('mp4', 500, 'ext'), leaf('', 400, 'ext'), leaf('', 300, 'files')]), 600, 400, 1)
    expect(rects.map((r) => r.label)).toEqual(['.mp4', '(no extension)', 'other files'])
  })

  it('nested extension blocks keep their folder colour', () => {
    const rects = layoutTreemap(root([leaf('q', 100), dir('p', [leaf('jpg', 300, 'ext'), leaf('', 200, 'files')])]), 800, 600, 2)
    const p = rects.find((r) => r.node.name === 'p')!
    for (const r of rects.filter((r) => r.parent === p.node)) expect(r.owner).toBe(p.owner)
  })

  it('labels only boxes bigger than 46 x 30: name through displayName, file count and size as meta', () => {
    const rects = layoutTreemap(root([leaf('a\nb', 999), leaf('tiny', 20)]), 600, 400, 1)
    const big = rects.find((r) => r.node.name === 'a\nb')!
    expect(big.label).toBe('a␊b')
    expect(big.meta).toBe('1 file | 999 B')
    expect(big.label).not.toContain('\n')
    expect(rects.find((r) => r.node.name === 'tiny')!.label).toBeNull()
  })

  it('nested levels keep the parent colour slot and a label strip', () => {
    const rects = layoutTreemap(root([dir('p', [leaf('x', 300), leaf('y', 200)]), leaf('q', 100)]), 800, 600, 2)
    const p = rects.find((r) => r.node.name === 'p')!
    const x = rects.find((r) => r.node.name === 'x')!
    expect(x.owner).toBe(p.owner)
    expect(x.y).toBeGreaterThanOrEqual(p.y + 18)
    expect(x.depth).toBe(2)
  })

  // One big folder and many small ones, each with a child: the small ones end up
  // shorter than the label strip.
  const crowded = root([
    dir('big', [leaf('b1', 90_000), leaf('b2', 60_000)]),
    ...Array.from({ length: 12 }, (_, i) => dir(`s${i}`, [dir(`s${i}k`, [leaf(`s${i}kk`, 40 + i)])])),
  ])

  it('keeps every rect inside its parent rect', () => {
    const rects = layoutTreemap(crowded, 600, 400, 3)
    const of = new Map(rects.map((r) => [r.node, r]))
    for (const r of rects) {
      const p = of.get(r.parent!)
      if (!p) continue
      expect(r.x).toBeGreaterThanOrEqual(p.x - 1e-6)
      expect(r.y).toBeGreaterThanOrEqual(p.y - 1e-6)
      expect(r.x + r.w).toBeLessThanOrEqual(p.x + p.w + 1e-6)
      expect(r.y + r.h).toBeLessThanOrEqual(p.y + p.h + 1e-6)
    }
  })

  it('drops rects thinner than 2 px, which would draw as lines', () => {
    const rects = layoutTreemap(crowded, 600, 400, 3)
    for (const r of rects) expect(Math.min(r.w, r.h), r.node.name).toBeGreaterThanOrEqual(2)
  })

  it('gives a container a label strip only when it is big enough to be labelled', () => {
    // p comes out about 32 x 100: tall enough for a strip, too narrow for a name.
    const rects = layoutTreemap(root([leaf('a', 96_000), dir('p', [leaf('x', 4_000)])]), 800, 100, 2)
    const p = rects.find((r) => r.node.name === 'p')!
    const x = rects.find((r) => r.node.name === 'x')!
    expect(p.label).toBeNull()
    expect(x.y).toBeLessThan(p.y + 18)
  })
})

describe('hitTest', () => {
  it('returns the deepest rect, not its container', () => {
    const rects = layoutTreemap(root([dir('p', [leaf('x', 300), leaf('y', 200)]), leaf('q', 100)]), 800, 600, 2)
    const x = rects.find((r) => r.node.name === 'x')!
    expect(hitTest(rects, x.x + x.w / 2, x.y + x.h / 2)?.node.name).toBe('x')
    expect(hitTest(rects, -5, -5)).toBeNull()
  })
})

describe('neighbour', () => {
  it('moves to the nearest leaf in the direction', () => {
    const rects = layoutTreemap(root([leaf('a', 500), leaf('b', 300), leaf('c', 200)]), 900, 300, 1)
    const left = rects.reduce((m, r) => (r.x < m.x ? r : m))
    const next = neighbour(rects, left, 'right')
    expect(next).not.toBeNull()
    expect(next!.x).toBeGreaterThan(left.x)
    expect(neighbour(rects, left, 'left')).toBeNull()
  })
})
