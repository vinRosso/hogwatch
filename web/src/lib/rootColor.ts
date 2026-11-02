// The root color scale is defined in styles/tokens.css as --root-1 … --root-6.
// Each root takes the next one from its id, so it keeps the same color on every screen.
const SCALE = 6

export function rootColor(id: number): string {
  return `var(--root-${((((id - 1) % SCALE) + SCALE) % SCALE) + 1})`
}
