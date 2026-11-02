<script lang="ts">
  import { api } from '../../lib/api/client'
  import type { Child, ChildrenPage, Node } from '../../lib/api/types'
  import { displayName } from '../../lib/fname'
  import { t } from '../../lib/i18n/en'
  import Button from '../ui/Button.svelte'
  import Dialog from '../ui/Dialog.svelte'

  /** Picks a folder of a root's current scan as an admin (names come from the database, never the filesystem). */
  let { open, rootId, onpick, onclose }: { open: boolean; rootId: number; onpick: (relPath: string) => void; onclose: () => void } = $props()

  let node = $state<Node | null>(null)
  let dirs = $state<Child[]>([])
  let cursor = $state<string | null>(null)
  let paging = $state(false)
  let moreFailed = $state(false)
  let failed = $state(false)
  let seq = 0

  async function show(ref: string | null) {
    const mine = ++seq
    failed = false
    moreFailed = false
    try {
      const r = ref ?? (await api.get<{ ref: string }>('/resolve', { root: rootId, path: '' })).ref
      const [n, page] = await Promise.all([
        api.get<Node>(`/nodes/${r}`),
        api.get<ChildrenPage>(`/nodes/${r}/children`, { sort: 'name', limit: 200 }),
      ])
      if (mine !== seq) return
      node = n
      dirs = page.items.filter((c) => c.kind === 'dir' && c.ref)
      cursor = page.next_cursor
    } catch {
      if (mine === seq) failed = true
    }
  }

  // The next page of the current folder; a move to another folder in between drops the result.
  async function more() {
    if (!node || !cursor || paging) return
    const mine = seq
    paging = true
    moreFailed = false
    try {
      const page = await api.get<ChildrenPage>(`/nodes/${node.ref}/children`, { sort: 'name', limit: 200, cursor })
      if (mine !== seq) return
      dirs = [...dirs, ...page.items.filter((c) => c.kind === 'dir' && c.ref)]
      cursor = page.next_cursor
    } catch {
      if (mine === seq) moreFailed = true
    } finally {
      paging = false
    }
  }

  $effect(() => {
    if (open) void show(null)
  })
</script>

<Dialog {open} title={t.admin.paths.pickTitle} {onclose}>
  {#if failed}
    <p role="alert">{t.admin.paths.noScan}</p>
  {:else if node}
    <p class="here mono" style="unicode-bidi: isolate">/{displayName(node.rel_path)}</p>
    <ul>
      {#if node.parent_ref}
        <li><button onclick={() => show(node!.parent_ref)}>..</button></li>
      {/if}
      {#each dirs as d (d.ref)}
        <li><button style="unicode-bidi: isolate" onclick={() => show(d.ref)}>{displayName(d.name)}</button></li>
      {/each}
    </ul>
    {#if cursor}<Button variant="ghost" disabled={paging} onclick={more}>{t.admin.paths.moreFolders}</Button>{/if}
    {#if moreFailed}<p role="alert">{t.admin.paths.moreFailed}</p>{/if}
    <div class="row">
      <Button variant="ghost" onclick={onclose}>{t.common.cancel}</Button>
      <Button
        onclick={() => {
          onpick(node!.rel_path)
          onclose()
        }}
      >
        {node.rel_path === '' ? t.admin.paths.pickRoot : t.admin.paths.pick}
      </Button>
    </div>
  {:else}
    <p>{t.common.loading}</p>
  {/if}
</Dialog>

<style>
  .here {
    margin: 0 0 8px;
    overflow-wrap: anywhere;
  }
  ul {
    list-style: none;
    margin: 0 0 12px;
    padding: 0;
    max-height: 300px;
    overflow: auto;
  }
  li button {
    width: 100%;
    text-align: left;
    border: none;
    background: none;
    padding: 5px 6px;
    cursor: pointer;
  }
  li button:hover,
  li button:focus-visible {
    background: var(--sunk);
  }
  .row {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
  }
</style>
