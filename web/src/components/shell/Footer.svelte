<script lang="ts">
  import { buildInfo } from '../../lib/buildinfo'
  import { t } from '../../lib/i18n/en'
</script>

<footer>
  <span>{t.footer.names}</span>
  <span class="mono">{buildInfo.version}</span>
  <a href={buildInfo.sourceURL} rel="noopener noreferrer" target="_blank">{t.footer.source}</a>
  <span>{t.footer.license}</span>
</footer>

<style>
  footer {
    display: flex;
    gap: 18px;
    flex-wrap: wrap;
    padding: 10px 20px;
    border-top: var(--edge);
    background: var(--card);
    font-size: 12px;
    color: var(--ink-2);
  }
</style>
