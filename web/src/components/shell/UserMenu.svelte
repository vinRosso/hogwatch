<script lang="ts">
  import { t } from '../../lib/i18n/en'
  import { session } from '../../lib/session.svelte'
  import UserName from '../ui/UserName.svelte'

  let open = $state(false)
</script>

<div class="menu">
  <button class="who" aria-haspopup="menu" aria-expanded={open} aria-label={t.user.menu} onclick={() => (open = !open)}>
    <UserName id={session.me?.id ?? 0} name={session.me?.display_name || session.me?.username || ''} />
  </button>
  {#if open}
    <div class="pop" role="menu">
      <a role="menuitem" href="/profile" onclick={() => (open = false)}>{t.user.profile}</a>
      <button role="menuitem" onclick={() => session.logout()}>{t.user.signOut}</button>
    </div>
  {/if}
</div>

<style>
  .menu {
    position: relative;
  }
  .who {
    border: var(--edge);
    border-radius: var(--r-pill);
    background: var(--card);
    padding: 4px 12px;
    cursor: pointer;
    max-width: 180px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .pop {
    position: absolute;
    right: 0;
    top: calc(100% + 6px);
    min-width: 150px;
    background: var(--card);
    border: var(--edge);
    border-radius: var(--r);
    box-shadow: var(--pop);
    overflow: hidden;
    display: grid;
    z-index: 40;
  }
  .pop a,
  .pop button {
    padding: 8px 12px;
    text-align: left;
    background: none;
    border: none;
    color: var(--ink);
    text-decoration: none;
    cursor: pointer;
  }
  .pop a:hover,
  .pop button:hover {
    background: var(--sunk);
  }
</style>
