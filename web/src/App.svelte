<script lang="ts">
  import { onMount } from 'svelte'
  import Footer from './components/shell/Footer.svelte'
  import Notice from './components/shell/Notice.svelte'
  import TopBar from './components/shell/TopBar.svelte'
  import Button from './components/ui/Button.svelte'
  import EmptyState from './components/ui/EmptyState.svelte'
  import Toast from './components/ui/Toast.svelte'
  import Browse from './routes/Browse.svelte'
  import Usage from './routes/Usage.svelte'
  import AdminLayout from './routes/admin/AdminLayout.svelte'
  import Listings from './routes/Listings.svelte'
  import Listing from './routes/Listing.svelte'
  import { t } from './lib/i18n/en'
  import { installLinkInterception, match, navigate, router } from './lib/router.svelte'
  import { installAuthRedirect, session } from './lib/session.svelte'
  import Login from './routes/Login.svelte'
  import NotFound from './routes/NotFound.svelte'
  import Profile from './routes/Profile.svelte'
  import Setup from './routes/Setup.svelte'

  const bare = $derived(router.path === '/login' || router.path === '/setup')

  onMount(() => {
    installAuthRedirect()
    const off = installLinkInterception()
    if (router.path !== '/setup') void session.load()
    return off
  })

  // Signed out (first load, sign-out then Back, expired session): go to the login page.
  $effect(() => {
    if (session.loaded && !session.me && !session.loadError && router.path !== '/login' && router.path !== '/setup') {
      navigate(`/login?next=${encodeURIComponent(router.path + location.search)}`, { replace: true })
    }
  })

  const adminRoute = $derived(match('/admin/*', router.path) !== null || router.path === '/admin')
  const listingId = $derived.by(() => {
    const id = match('/listings/:id', router.path)?.id
    return id !== undefined && /^[0-9]+$/.test(id) ? Number(id) : null
  })
  // The browse view fills the window; its panels scroll inside it instead of the page.
  const browsing = $derived(router.path === '/browse' || router.path.startsWith('/browse/'))
  $effect(() => {
    if (session.me && router.path === '/') navigate('/browse', { replace: true })
  })
</script>

{#if router.path === '/login'}
  <Login />
{:else if router.path === '/setup'}
  <Setup />
{:else if !bare}
  {#if session.me}
    <div class="shell" class:fit={browsing}>
      <TopBar />
      <Notice />
      <div class="main">
        {#if router.path === '/profile'}
          <Profile />
        {:else if router.path === '/usage'}
          <Usage />
        {:else if (router.path === '/listings' || listingId !== null) && !session.canListFiles()}
          <NotFound />
        {:else if router.path === '/listings'}
          <Listings />
        {:else if listingId !== null}
          <Listing id={listingId} />
        {:else if adminRoute && !session.me.is_admin}
          <NotFound />
        {:else if adminRoute}
          <AdminLayout />
        {:else if router.path === '/browse' || router.path.startsWith('/browse/')}
          <Browse />
        {:else}
          <NotFound />
        {/if}
      </div>
      <Footer />
    </div>
  {:else if session.loadError}
    <EmptyState title={t.common.error} body={session.loadError}>
      <Button onclick={() => session.load()}>{t.common.retry}</Button>
    </EmptyState>
  {:else}
    <p class="pad">{t.common.loading}</p>
  {/if}
{/if}
<Toast />

<style>
  .shell {
    min-height: 100vh;
    display: flex;
    flex-direction: column;
  }
  .main {
    flex: 1;
    min-width: 0;
  }
  /* Browse fits the window only side by side; stacked (see Browse.svelte) the page scrolls. */
  @media (min-width: 1001px) {
    .shell.fit {
      height: 100vh;
    }
    .shell.fit .main {
      display: flex;
      flex-direction: column;
      min-height: 0;
    }
  }
  .pad {
    padding: 24px;
  }
</style>
