const known: Record<number, string> = {
  13: 'permission denied',
  2: 'vanished during the scan',
  5: 'I/O error',
  40: 'too many symbolic links',
  36: 'name too long',
  18: 'other filesystem, skipped',
}

export function errnoMessage(errno: number): string {
  return known[errno] ?? `errno ${errno}`
}
