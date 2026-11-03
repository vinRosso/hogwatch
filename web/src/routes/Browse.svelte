<script lang="ts">
  import { untrack } from 'svelte'
  import EmptyState from '../components/ui/EmptyState.svelte'
  import Button from '../components/ui/Button.svelte'
  import Toolbar from '../components/browse/Toolbar.svelte'
  import RightPanel from '../components/browse/RightPanel.svelte'
  import Treemap from '../components/browse/Treemap.svelte'
  import ExportMenu from '../components/browse/ExportMenu.svelte'
  import ListingDialog from '../components/browse/ListingDialog.svelte'
  import RootsOverview from './admin/RootsOverview.svelte'
  import UsersOverview from './admin/UsersOverview.svelte'
  import { filterToParam } from '../lib/filter'
  import { browse } from '../lib/browse.svelte'
  import { t } from '../lib/i18n/en'
  import { match, router } from '../lib/router.svelte'
  import { session } from '../lib/session.svelte'
  import { loadOwners } from '../lib/owners.svelte'

  const ref = $derived(match('/browse/:ref', router.path)?.ref ?? null)
  let listRef = $state<string | null>(null)
  let listMode = $state<'children' | 'subtree'>('children')

  // The side panel's width, dragged by its left edge and kept in the account's prefs.
  const PANEL_MIN = 320
  const PANEL_DEFAULT = 420
  let vw = $state(window.innerWidth)
  const clampPanel = (w: number) => Math.round(Math.min(Math.max(PANEL_MIN, Math.min(900, vw * 0.6)), Math.max(PANEL_MIN, w)))
  // The width while it is being changed; once saved, the prefs hold it (setPrefs updates them at once).
  let dragged = $state<number | null>(null)
  const panelW = $derived(dragged ?? clampPanel(session.prefs().panel_width ?? PANEL_DEFAULT))
  let dragging = $state(false)

  function savePanel() {
    if (dragged === null) return
    session.setPrefs({ panel_width: dragged })
    dragged = null
  }

  function dragStart(e: PointerEvent) {
    if (e.button !== 0) return
    const x0 = e.clientX
    const w0 = panelW
    const el = e.currentTarget as HTMLElement
    el.setPointerCapture(e.pointerId)
    dragging = true
    const move = (m: PointerEvent) => (dragged = clampPanel(w0 + x0 - m.clientX))
    const end = () => {
      dragging = false
      el.removeEventListener('pointermove', move)
      el.removeEventListener('pointerup', end)
      el.removeEventListener('pointercancel', end)
      savePanel()
    }
    el.addEventListener('pointermove', move)
    el.addEventListener('pointerup', end)
    el.addEventListener('pointercancel', end)
  }

  function dragKey(e: KeyboardEvent) {
    const step = e.shiftKey ? 60 : 20
    if (e.key === 'ArrowLeft') dragged = clampPanel(panelW + step)
    else if (e.key === 'ArrowRight') dragged = clampPanel(panelW - step)
    else if (e.key === 'Home') dragged = PANEL_DEFAULT
    else return
    e.preventDefault()
  }

  // Saved on release, so holding a key sends one request, not one per repeat.
  function dragKeyUp(e: KeyboardEvent) {
    if (['ArrowLeft', 'ArrowRight', 'Home'].includes(e.key)) savePanel()
  }

  // Folder colors come from the user paths; the views redraw when they load.
  $effect(() => {
    void session.me?.id
    void session.roots
    untrack(() => void loadOwners())
  })

  // Re-run only when the ref or the selected root changes, not on every state write.
  $effect(() => {
    const r = ref
    const rootId = session.rootId
    void rootId
    untrack(() => {
      browse.applyQuery(router.query)
      if (r !== null && browse.node?.ref === r) return
      void browse.start(r)
    })
  })

  // The root picker moved to another root: go to that root's start folder.
  $effect(() => {
    const rootId = session.rootId
    untrack(() => {
      if (browse.node && rootId !== null && browse.node.root_id !== rootId) void browse.start(null)
    })
  })

  // Filter and depth live in the URL.
  $effect(() => {
    const q = router.query
    untrack(() => browse.applyQuery(q))
  })
</script>

<svelte:window bind:innerWidth={vw} />

<div class="browse">
  {#if browse.notice}<p class="notice" role="status">{browse.notice}</p>{/if}
  {#if browse.status === 'empty'}
    <EmptyState title={t.nav.browse} body={browse.emptyReason} />
  {:else if browse.status === 'error'}
    <EmptyState title={t.common.error} body={browse.emptyReason}>
      <Button onclick={() => browse.start(ref)}>{t.common.retry}</Button>
    </EmptyState>
  {:else}
    <div class="split" class:dragging style:--panel-w={`${panelW}px`}>
      <section class="left" aria-busy={browse.status === 'loading'}>
        {#if session.me?.is_admin}
          <div class="overview">
            <UsersOverview />
            <RootsOverview />
          </div>
        {/if}
        <div class="card">
          <Toolbar>
            {#snippet actions()}{#if browse.node}<ExportMenu ref={browse.node.ref} />{/if}{/snippet}
          </Toolbar>
          {#if browse.node}
            <Treemap
              ref={browse.node.ref}
              root_id={browse.node.root_id}
              rel_path={browse.node.rel_path}
              filter={filterToParam(browse.filter)}
              depth={browse.depth}
            />
          {/if}
        </div>
      </section>
      <aside class="right">
        <!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_no_noninteractive_element_interactions -->
        <div
          class="grip"
          role="separator"
          aria-orientation="vertical"
          aria-label={t.browse.resizePanel}
          aria-valuenow={panelW}
          aria-valuemin={PANEL_MIN}
          tabindex="0"
          onpointerdown={dragStart}
          onkeydown={dragKey}
          onkeyup={dragKeyUp}
          ondblclick={() => ((dragged = PANEL_DEFAULT), savePanel())}
        ></div>
        <RightPanel onlist={(r, mode) => ((listRef = r), (listMode = mode ?? 'children'))} />
      </aside>
    </div>
  {/if}
</div>

<ListingDialog open={listRef !== null} ref={listRef ?? ''} initialMode={listMode} onclose={() => (listRef = null)} />

<style>
  .browse {
    display: flex;
    flex: 1;
    flex-direction: column;
    min-height: 0;
  }
  .notice {
    margin: 0;
    padding: 6px 20px;
    background: var(--mustard-bg);
    border-bottom: var(--edge);
  }
  .split {
    display: grid;
    flex: 1;
    grid-template-columns: minmax(0, 1fr) var(--panel-w, 420px);
    grid-template-rows: minmax(0, 1fr);
    gap: 16px;
    min-height: 0;
    padding: 16px 20px;
  }
  .split.dragging {
    cursor: col-resize;
    user-select: none;
  }
  .left,
  .right {
    display: flex;
    flex-direction: column;
    min-width: 0;
    min-height: 0;
  }
  .right {
    position: relative;
  }
  /* A strip in the gap left of the panel; wider than it looks so it is easy to grab. */
  .grip {
    position: absolute;
    top: 0;
    bottom: 0;
    left: -14px;
    width: 12px;
    cursor: col-resize;
    touch-action: none;
    border-radius: 6px;
    z-index: 1;
  }
  .grip:hover,
  .grip:focus-visible,
  .dragging .grip {
    background: var(--hair);
  }
  .overview {
    display: grid;
    flex: 0 0 auto;
    grid-template-columns: repeat(auto-fit, minmax(300px, 1fr));
    gap: 14px;
    margin-bottom: 16px;
  }
  .card {
    display: flex;
    flex: 1;
    flex-direction: column;
    min-height: 0;
    border: var(--edge);
    border-radius: var(--r);
    background: var(--card);
    box-shadow: var(--pop);
    overflow: hidden;
  }
  .card > :global(.wrap) {
    flex: 1;
    height: auto;
    min-height: 0;
    border: none;
    border-radius: 0;
  }
  @media (max-width: 1000px) {
    .split {
      grid-template-columns: minmax(0, 1fr);
      grid-template-rows: auto;
    }
    .grip {
      display: none;
    }
    /* Stacked the page has no fixed height to share out, so the treemap takes its own. */
    .card > :global(.wrap) {
      flex: none;
      height: min(66vh, 640px);
    }
  }
</style>
