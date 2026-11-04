<script lang="ts">
  import type { Snippet } from 'svelte'
  import { t } from '../../lib/i18n/en'

  let { children }: { children: Snippet } = $props()
</script>

<main class="page">
  <div class="stack">
    <img class="logo" src="/favicon.svg" alt="" width="88" height="78" />
    <h1 class="brand">{t.appName}</h1>
    {@render children()}
  </div>
</main>

<style>
  .page {
    min-height: 100vh;
    min-height: 100dvh;
    background-image: repeating-linear-gradient(
      -45deg,
      rgb(31 27 22 / 0.045) 0 1px,
      transparent 1px 9px
    );
    display: flex;
    align-items: center;
    justify-content: center;
    /* Optical centre: the block sits a little above the geometric middle, which looks low. */
    padding: 0 16px 12vh;
  }
  .stack {
    width: 100%;
    max-width: 380px;
  }
  .logo {
    display: block;
    margin: 0 auto 12px;
  }
  .brand {
    font-size: 48px;
    font-weight: 700;
    text-align: center;
    line-height: 1.1;
    margin: 0 0 24px;
  }
</style>
