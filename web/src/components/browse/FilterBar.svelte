<script lang="ts">
  import { browse } from '../../lib/browse.svelte'
  import { ages, bucketRuns, categories, sizes, type Filter } from '../../lib/filter'
  import { t } from '../../lib/i18n/en'

  type Key = 'cat' | 'age' | 'size'
  type RangeKey = 'age' | 'size'
  const ranged: Record<RangeKey, readonly string[]> = { age: ages, size: sizes }
  const copy = { age: t.filter.range.age, size: t.filter.range.size }
  let menu = $state<Key | null>(null)

  function set(k: Key, toks: string[]) {
    const next: Filter = { cat: [...browse.filter.cat], age: [...browse.filter.age], size: [...browse.filter.size] }
    ;(next as Record<Key, string[]>)[k] = toks
    browse.setFilter(next)
  }

  function toggle(v: string) {
    const list = [...browse.filter.cat] as string[]
    const i = list.indexOf(v)
    if (i >= 0) list.splice(i, 1)
    else list.push(v)
    set('cat', list)
  }

  // Age and size take one threshold at a time: picking one replaces the last. A bucket range is the
  // run of buckets below or above a bound, which is what the server filters on.
  function choose(k: RangeKey, from: number, to: number) {
    set(k, ranged[k].slice(from, to + 1))
  }

  function chosen(k: RangeKey, from: number, to: number): boolean {
    const runs = bucketRuns(ranged[k], browse.filter[k])
    return runs.length === 1 && runs[0][0] === from && runs[0][1] === to
  }

  function rangeLabel(k: RangeKey, from: number, to: number): string {
    const { bounds, any, under, atLeast, between } = copy[k]
    if (from === 0 && to === bounds.length) return any
    if (from === 0) return under(bounds[to])
    if (to === bounds.length) return atLeast(bounds[from - 1])
    return between(bounds[from - 1], bounds[to])
  }

  const active = $derived.by(() => {
    const out: { id: string; label: string; remove: () => void }[] = []
    for (const v of browse.filter.cat) out.push({ id: 'cat' + v, label: t.filter.cat[v], remove: () => toggle(v) })
    for (const k of ['age', 'size'] as const) {
      for (const [from, to] of bucketRuns(ranged[k], browse.filter[k])) {
        const gone = new Set(ranged[k].slice(from, to + 1))
        out.push({
          id: `${k}${from}-${to}`,
          label: rangeLabel(k, from, to),
          remove: () =>
            set(
              k,
              browse.filter[k].filter((v) => !gone.has(v)),
            ),
        })
      }
    }
    return out
  })
</script>

<div class="bar" role="group" aria-label={t.filter.label}>
  {#each active as p (p.id)}
    <span class="pill">
      {p.label}
      <button aria-label={t.filter.remove(p.label)} onclick={p.remove}>×</button>
    </span>
  {/each}
  {#each ['cat', 'age', 'size'] as const as k (k)}
    <span class="add">
      <button class="addbtn" aria-haspopup="true" aria-expanded={menu === k} onclick={() => (menu = menu === k ? null : k)}>
        {t.filter.add[k]}
      </button>
      {#if menu === k}
        {#if k === 'cat'}
          <div class="menu">
            {#each categories as v (v)}
              <label>
                <input type="checkbox" checked={browse.filter.cat.includes(v)} onchange={() => toggle(v)} />
                {t.filter.cat[v]}
              </label>
            {/each}
          </div>
        {:else}
          {@const { bounds, under, atLeast } = copy[k]}
          <div class="menu table">
            <span></span>
            <span class="head">{t.filter.underHead}</span>
            <span class="head">{t.filter.atLeastHead}</span>
            {#each bounds as b, i (b)}
              <span class="bound">{b}</span>
              <input
                type="radio"
                name={k}
                aria-label={under(b)}
                checked={chosen(k, 0, i)}
                onchange={() => choose(k, 0, i)}
              />
              <input
                type="radio"
                name={k}
                aria-label={atLeast(b)}
                checked={chosen(k, i + 1, bounds.length)}
                onchange={() => choose(k, i + 1, bounds.length)}
              />
            {/each}
          </div>
        {/if}
      {/if}
    </span>
  {/each}
</div>

<style>
  .bar {
    display: flex;
    gap: 6px;
    flex-wrap: wrap;
    align-items: center;
  }
  .pill {
    border: var(--edge);
    border-radius: var(--r-pill);
    background: var(--mustard-bg);
    padding: 1px 4px 1px 10px;
    font-size: 12px;
  }
  .pill button {
    border: none;
    background: none;
    cursor: pointer;
  }
  .addbtn {
    border: 1px dashed var(--ink);
    border-radius: var(--r-pill);
    background: none;
    padding: 1px 10px;
    font-size: 12px;
    cursor: pointer;
  }
  .add {
    position: relative;
  }
  .menu {
    position: absolute;
    top: calc(100% + 4px);
    left: 0;
    z-index: 30;
    background: var(--card);
    border: var(--edge);
    border-radius: var(--r);
    box-shadow: var(--pop);
    padding: 6px 10px;
    display: grid;
    gap: 2px;
    min-width: 180px;
  }
  .table {
    grid-template-columns: 1fr auto auto;
    column-gap: 16px;
    align-items: center;
    min-width: 220px;
  }
  .head {
    font-size: 12px;
    text-align: center;
    opacity: 0.7;
  }
  .table input {
    justify-self: center;
  }
</style>
