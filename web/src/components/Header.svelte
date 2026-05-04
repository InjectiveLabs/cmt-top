<script lang="ts">
  import type { Writable } from "svelte/store";
  import { chain } from "../lib/stores";
  export let status: Writable<"connecting"|"open"|"reconnecting"|"closed"> | undefined;
  $: s = status ? $status : "connecting";
</script>

<header class="panel" style="display:flex; gap:24px; align-items:center; justify-content:space-between;">
  <div style="display:flex; gap:24px; align-items:baseline;">
    <span style="font-weight:700; color: var(--accent); font-size: 15px;">injective-top</span>
    <span>{$chain.chain?.network ?? "—"}</span>
    <span class="kv">
      <span class="k">h</span>
      <span class="v">{$chain.height}</span>
    </span>
    <span class="kv">
      <span class="k">r</span>
      <span class="v">{$chain.round}</span>
    </span>
    <span class="kv">
      <span class="k">step</span>
      <span class="v">{$chain.step}</span>
    </span>
    {#if $chain.blockTime}<span class="k">avg blk: {($chain.blockTime/1000).toFixed(2)}s</span>{/if}
  </div>
  <div>
    <span class="connection {s === 'open' ? '' : s === 'connecting' ? 'connecting' : 'lost'}">
      ● {s}
    </span>
  </div>
</header>
