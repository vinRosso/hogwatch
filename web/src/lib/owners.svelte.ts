import { api } from './api/client'
import type { Node, OwnedPath } from './api/types'
import { session } from './session.svelte'

// A folder that a user path assigns to a user. rel_path '' is the whole root.
export type Owned = { user_id: number; root_id: number; rel_path: string }

let owned = $state.raw<Owned[]>([])

/** The assigned folders, read by the views so they redraw when they load. */
export function owners(): Owned[] {
  return owned
}

/**
 * Fills the folder owners. An admin gets every user's paths; anyone else gets only their own,
 * resolved from the roots' anchors, since other users' paths are not theirs to see.
 */
export async function loadOwners(): Promise<void> {
  const me = session.me
  if (!me) {
    owned = []
    return
  }
  try {
    if (me.is_admin) {
      // One response holds every user's assignments, whatever the number of users.
      owned = (await api.get<{ items: OwnedPath[] }>('/admin/user-paths')).items
    } else {
      const anchors = session.roots.flatMap((r) => r.anchors.filter((a) => a.found && a.ref).map((a) => ({ root_id: r.id, ref: a.ref! })))
      owned = await Promise.all(
        anchors.map(async (a) => ({ user_id: me.id, root_id: a.root_id, rel_path: (await api.get<Node>(`/nodes/${a.ref}`)).rel_path })),
      )
    }
  } catch {
    owned = []
  }
}

/** The user owning a folder: the user path that covers it most specifically, or null. */
export function ownerOf(root_id: number, rel_path: string): number | null {
  let best: number | null = null
  let len = -1
  for (const o of owned) {
    if (o.root_id !== root_id) continue
    const covers = o.rel_path === '' || rel_path === o.rel_path || rel_path.startsWith(o.rel_path + '/')
    if (covers && o.rel_path.length > len) {
      best = o.user_id
      len = o.rel_path.length
    }
  }
  return best
}
