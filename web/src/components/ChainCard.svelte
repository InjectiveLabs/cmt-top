<script lang="ts">
  import { chain, errors, validatorsOrdered } from "../lib/stores";

  $: total = $validatorsOrdered.length;
  $: prevoted = $validatorsOrdered.filter(v => v.prevote.kind === "voted").length;
  $: precommitted = $validatorsOrdered.filter(v => v.precommit.kind === "voted").length;
  $: prevotePct = total ? (prevoted / total) * 100 : 0;
  $: precommitPct = total ? (precommitted / total) * 100 : 0;
</script>

<div class="panel">
  <h2>chain</h2>
  <div class="kv">
    <span class="k">network</span>
    <span class="v">{$chain.chain?.network ?? "—"}</span>
    <span class="k">comet</span>
    <span class="v">{$chain.chain?.cometVersion ?? "—"}</span>
    <span class="k">rpc</span>
    <span class="v" style="font-size:11px; color: var(--muted);">{$chain.activeRPC ?? "—"}</span>
    <span class="k">our val</span>
    <span class="v" style="font-size:11px;">{$chain.chain?.ourValidator?.slice(0,12) ?? "—"}</span>
    {#if $chain.upgrade}
      <span class="k">upgrade</span>
      <span class="v" style="color: var(--warn);">{$chain.upgrade.name} @ {$chain.upgrade.height}</span>
    {/if}
  </div>
</div>

<div class="panel">
  <h2>consensus</h2>
  <div style="display:flex; flex-direction:column; gap:6px;">
    <div>
      <div style="display:flex; justify-content:space-between; font-size:11px;">
        <span class="k">prevote</span>
        <span class="v">{prevotePct.toFixed(1)}% ({prevoted}/{total})</span>
      </div>
      <div class="bar"><span style="width:{prevotePct}%; background:var(--accent);"></span></div>
    </div>
    <div>
      <div style="display:flex; justify-content:space-between; font-size:11px;">
        <span class="k">precommit</span>
        <span class="v">{precommitPct.toFixed(1)}% ({precommitted}/{total})</span>
      </div>
      <div class="bar"><span style="width:{precommitPct}%; background:var(--good);"></span></div>
    </div>
  </div>
</div>

{#if Object.keys($errors).length > 0}
  <div class="panel">
    <h2>errors</h2>
    {#each Object.entries($errors) as [k,v]}
      <div style="font-size:11px; color: var(--bad);">{k}: {v}</div>
    {/each}
  </div>
{/if}
