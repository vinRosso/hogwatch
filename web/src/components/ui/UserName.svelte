<script lang="ts">
  import { displayName } from '../../lib/fname'
  import { userColor } from '../../lib/userColor'

  let { id, name }: { id: number; name: string } = $props()
</script>

<span class="user-name" style:--user={userColor(id)}><span class="dot" aria-hidden="true"></span><span style="unicode-bidi: isolate">{displayName(name)}</span></span>

<style>
  .user-name {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
    max-width: 100%;
  }
  .dot {
    flex: none;
    width: 10px;
    height: 10px;
    border-radius: 50%;
    background: var(--user);
  }
</style>
