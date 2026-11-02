const repo = 'https://github.com/vinRosso/hogwatch'

/** The source link points at the exact commit only for a plain hex commit id. */
export function sourceURLFor(commit: string): string {
  return /^[0-9a-f]{7,40}$/i.test(commit) ? `${repo}/tree/${commit}` : repo
}

export const buildInfo = {
  version: __HW_VERSION__,
  commit: __HW_COMMIT__,
  sourceURL: sourceURLFor(__HW_COMMIT__),
}
