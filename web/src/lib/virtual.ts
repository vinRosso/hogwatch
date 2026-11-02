/** Rows to render for a scroll position: the visible ones plus `overscan` on each side. */
export function visibleRange(
  scrollTop: number,
  viewport: number,
  rowH: number,
  count: number,
  overscan = 10,
): { start: number; end: number } {
  const start = Math.max(0, Math.floor(scrollTop / rowH) - overscan)
  const end = Math.min(count, Math.ceil((scrollTop + viewport) / rowH) + overscan)
  return { start, end: Math.max(start, end) }
}
