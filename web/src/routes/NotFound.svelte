<script lang="ts">
  import EmptyState from '../components/ui/EmptyState.svelte'
  import { t } from '../lib/i18n/en'
</script>

<EmptyState title={t.notFound.title} body={t.notFound.body}>
  <a href="/browse">{t.notFound.back}</a>
</EmptyState>
