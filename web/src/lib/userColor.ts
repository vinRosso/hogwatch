// The user color scale is defined in styles/tokens.css as --user-1 … --user-6.
// Each user takes the next one from its id, so it keeps the same color on every screen.
const SCALE = 6

/** The token name of a user's color, such as --user-3. */
export function userVar(id: number): string {
  return `--user-${((((id - 1) % SCALE) + SCALE) % SCALE) + 1}`
}

export function userColor(id: number): string {
  return `var(${userVar(id)})`
}

/** A top-level block's color for non-admins: the scale in the same order as users and roots, by position (0 for the first). */
export function blockVar(index: number): string {
  return userVar(index + 1)
}

/** A folder with no user path: a color no user has (--unowned). */
export function ownerColor(owner: number | null): string {
  return owner === null ? 'var(--unowned)' : userColor(owner)
}
