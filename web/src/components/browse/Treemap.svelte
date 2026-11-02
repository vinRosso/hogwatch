<script lang="ts">
  import { onMount } from 'svelte'
  import { api } from '../../lib/api/client'
  import type { TreemapNode } from '../../lib/api/types'
  import { browse } from '../../lib/browse.svelte'
  import { displayName } from '../../lib/fname'
  import { formatBytes, formatCount } from '../../lib/format'
  import { t } from '../../lib/i18n/en'
  import { hitTest, layoutTreemap, neighbour, type Rect } from '../../lib/treemap'
  import { owners, ownerOf } from '../../lib/owners.svelte'
  import { session } from '../../lib/session.svelte'
  import { blockVar, userVar } from '../../lib/userColor'
  import EmptyState from '../ui/EmptyState.svelte'

  let {
    ref,
    root_id,
    rel_path,
    filter,
    depth,
  }: { ref: string; root_id: number; rel_path: string; filter: string; depth: number } = $props()

  let canvas: HTMLCanvasElement | undefined = $state()
  let box: HTMLDivElement | undefined = $state()
  let tree = $state.raw<TreemapNode | null>(null)
  // The folder the loaded tree belongs to. It changes with the tree, not with the browsed node,
  // which moves ahead of the tree when the root changes.
  let scope = $state.raw({ root_id: -1, rel_path: '' })
  let failed = $state(false)
  let size = $state({ w: 0, h: 0 })
  let rects: Rect[] = []
  let hovered: Rect | null = null
  // The resolved colors of the user scale and the unowned grey, by token name.
  const palette = new Map<string, string>()
  let ink = '#000'
  let card = '#fff'
  let paper = '#fff'
  let seq = 0

  const SCALE_TOKENS = Array.from({ length: 6 }, (_, i) => `--user-${i + 1}`)

  function readColors() {
    const cs = getComputedStyle(document.documentElement)
    const v = (n: string) => cs.getPropertyValue(n).trim() || '#888'
    for (const n of [...SCALE_TOKENS, '--unowned']) palette.set(n, v(n))
    ink = v('--ink')
    card = v('--card')
    paper = v('--paper')
  }

  const rgb = (hex: string): [number, number, number] => {
    const m = /^#([0-9a-f]{6})$/i.exec(hex)
    if (!m) return [136, 136, 136]
    const n = parseInt(m[1], 16)
    return [(n >> 16) & 255, (n >> 8) & 255, n & 255]
  }
  const mix = (a: string, b: string, k: number) => {
    const x = rgb(a)
    const y = rgb(b)
    return `rgb(${x.map((c, i) => Math.round(c * (1 - k) + y[i] * k)).join(',')})`
  }
  const dark = (hex: string) => {
    const [r, g, b] = rgb(hex)
    return (0.299 * r + 0.587 * g + 0.114 * b) / 255 < 0.55
  }

  // Non-admins get the Hogwatch palette, not an owner's color. A top-level block takes the scale color its
  // name hashes to, and everything inside it takes the same color, so each block reads as one group.
  const blockToken = new Map<TreemapNode, string>()
  function paintBlock(node: TreemapNode, token: string) {
    blockToken.set(node, token)
    for (const c of node.children) paintBlock(c, token)
  }
  function paintBlocks(root: TreemapNode) {
    blockToken.clear()
    root.children.forEach((c, i) => paintBlock(c, blockVar(i)))
  }

  function fit(ctx: CanvasRenderingContext2D, s: string, max: number): string {
    if (ctx.measureText(s).width <= max) return s
    let lo = 0
    let hi = s.length
    while (lo < hi) {
      const mid = (lo + hi + 1) >> 1
      if (ctx.measureText(s.slice(0, mid) + '…').width <= max) lo = mid
      else hi = mid - 1
    }
    return lo > 0 ? s.slice(0, lo) + '…' : ''
  }

  function draw() {
    if (!canvas || !size.w) return
    const dpr = window.devicePixelRatio || 1
    canvas.width = Math.round(size.w * dpr)
    canvas.height = Math.round(size.h * dpr)
    const ctx = canvas.getContext('2d')
    if (!ctx) return
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
    ctx.clearRect(0, 0, size.w, size.h)
    ctx.font = '12px "Space Grotesk", system-ui, sans-serif'
    ctx.textBaseline = 'top'
    for (const r of rects) {
      const base = session.me?.is_admin
        ? (palette.get(r.owner === null ? '--unowned' : userVar(r.owner)) ?? '#888')
        : (palette.get(blockToken.get(r.node) ?? blockVar(0)) ?? '#888')
      const container = r.node.children.length > 0
      const k = r.depth === 1 ? 0 : Math.min(0.5, 0.1 * (r.sib + 1))
      const fill = container ? mix(base, card, 0.35) : mix(base, card, k)
      const outline = new Path2D()
      outline.roundRect(r.x + 0.5, r.y + 0.5, Math.max(r.w - 1, 0), Math.max(r.h - 1, 0), 5)
      ctx.fillStyle = fill
      ctx.fill(outline)
      // Hatched: everything that is not a folder (extension blocks, other files, the rest).
      if (r.node.kind !== 'dir') {
        ctx.save()
        ctx.clip(outline)
        ctx.strokeStyle = ink
        ctx.globalAlpha = 0.25
        ctx.lineWidth = 1
        for (let d = -r.h; d < r.w; d += 8) {
          ctx.beginPath()
          ctx.moveTo(r.x + d, r.y + r.h)
          ctx.lineTo(r.x + d + r.h, r.y)
          ctx.stroke()
        }
        ctx.restore()
      }
      ctx.strokeStyle = ink
      ctx.lineWidth = r === hovered ? 2 : 1
      ctx.stroke(outline)
      if (r.label) {
        ctx.fillStyle = dark(base) && !container && k === 0 ? paper : ink
        // The meta sits at the right edge; the name gets what is left of the block.
        const meta = r.meta && ctx.measureText(r.meta).width <= r.w - 12 ? r.meta : null
        const room = meta ? r.w - 12 - ctx.measureText(meta).width - 8 : r.w - 12
        ctx.fillText(fit(ctx, r.label, room), r.x + 6, r.y + 5)
        if (meta) {
          ctx.textAlign = 'right'
          ctx.fillText(meta, r.x + r.w - 6, r.y + 5)
          ctx.textAlign = 'left'
        }
      }
    }
  }

  function relayout() {
    // Each block's folder is the loaded tree's path plus the names down to the block.
    const owner = (names: string[]) => ownerOf(scope.root_id, [scope.rel_path, ...names].filter(Boolean).join('/'))
    if (tree && !session.me?.is_admin) paintBlocks(tree)
    rects = tree && size.w ? layoutTreemap(tree, size.w, size.h, depth, owner) : []
    hovered = null
    draw()
  }

  async function load(r: string, f: string, d: number, s: { root_id: number; rel_path: string }) {
    const mine = ++seq
    failed = false
    try {
      const res = await api.get<TreemapNode>(`/nodes/${r}/treemap`, { depth: d, filter: f || undefined })
      if (mine !== seq) return
      scope = s
      tree = res
    } catch {
      if (mine === seq) failed = true
    }
  }

  $effect(() => {
    void load(ref, filter, depth, { root_id, rel_path })
  })
  $effect(() => {
    void tree
    void scope
    void size.w
    void size.h
    void depth
    void owners()
    relayout()
  })

  onMount(() => {
    readColors()
    const ro = new ResizeObserver((es) => {
      const c = es[0].contentRect
      size = { w: Math.floor(c.width), h: Math.floor(c.height) }
    })
    if (box) ro.observe(box)
    return () => ro.disconnect()
  })

  let frame = 0
  function move(e: PointerEvent) {
    if (frame) return
    const x = e.offsetX
    const y = e.offsetY
    frame = requestAnimationFrame(() => {
      frame = 0
      const r = hitTest(rects, x, y)
      if (r !== hovered) {
        hovered = r
        draw()
      }
    })
  }

  // A click enters the clicked directory; files and the rest block have no ref.
  function click(e: MouseEvent) {
    const r = hitTest(rects, e.offsetX, e.offsetY)
    if (r && r.node.kind === 'dir' && r.node.ref) browse.open(r.node.ref)
  }

  function key(e: KeyboardEvent) {
    const dirs: Record<string, 'left' | 'right' | 'up' | 'down'> = {
      ArrowLeft: 'left',
      ArrowRight: 'right',
      ArrowUp: 'up',
      ArrowDown: 'down',
    }
    if (e.key === 'Enter') {
      if (hovered?.node.kind === 'dir' && hovered.node.ref) browse.open(hovered.node.ref)
      return
    }
    const d = dirs[e.key]
    if (!d) return
    e.preventDefault()
    const leaves = rects.filter((r) => !r.node.children.length)
    const next = hovered ? neighbour(leaves, hovered, d) : (leaves[0] ?? null)
    if (next) {
      hovered = next
      draw()
    }
  }

  const summary = $derived(tree ? t.treemap.summary(displayName(tree.name), formatBytes(tree.bytes_alloc), formatCount(tree.children.length)) : t.treemap.loading)
</script>

<div class="wrap" bind:this={box}>
  {#if failed}
    <EmptyState title={t.common.error} body={t.treemap.failed} />
  {:else if tree && tree.children.length === 0}
    <EmptyState title={t.treemap.emptyTitle} body={t.treemap.empty} />
  {/if}
  <!-- svelte-ignore a11y_no_interactive_element_to_noninteractive_role -->
  <canvas
    bind:this={canvas}
    role="img"
    aria-label={summary}
    tabindex="0"
    style:width={`${size.w}px`}
    style:height={`${size.h}px`}
    onpointermove={move}
    onpointerleave={() => {
      hovered = null
      draw()
    }}
    onclick={click}
    onkeydown={key}
  ></canvas>
</div>

<style>
  .wrap {
    position: relative;
    width: 100%;
    height: min(66vh, 640px);
    min-height: 320px;
    box-sizing: border-box;
    padding: 10px;
    border: var(--edge);
    border-radius: var(--r);
    background: var(--card);
    overflow: hidden;
  }
  canvas {
    display: block;
    cursor: pointer;
  }
  .wrap :global(.empty) {
    position: absolute;
    inset: 0;
    display: grid;
    align-content: center;
  }
</style>
