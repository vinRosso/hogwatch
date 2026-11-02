import { ApiError } from './api/client'
import { displayName } from './fname'

/** The server's problem detail made safe to show (it can name a user or a path), else the fallback. */
export function problemText(e: unknown, fallback: string): string {
  return e instanceof ApiError && e.detail ? displayName(e.detail) : fallback
}
