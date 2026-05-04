<script lang="ts">
  import { validatorsOrdered, chain, type Validator } from "../lib/stores";

  let search = "";
  let sort: "power" | "moniker" | "missrate" = "power";
  let sortDir: 1 | -1 = -1;

  function setSort(s: "power" | "moniker" | "missrate") {
    if (sort === s) sortDir = (sortDir === 1 ? -1 : 1);
    else { sort = s; sortDir = -1; }
  }

  $: filtered = $validatorsOrdered.filter((v) => {
    if (!search) return true;
    const s = search.toLowerCase();
    return (v.moniker ?? "").toLowerCase().includes(s) || v.address.toLowerCase().includes(s);
  });
  $: rows = [...filtered].sort((a, b) => {
    let cmp = 0;
    switch (sort) {
      case "power": cmp = Number(a.votingPower) - Number(b.votingPower); break;
      case "moniker": cmp = (a.moniker ?? "").localeCompare(b.moniker ?? ""); break;
    }
    return cmp * sortDir;
  });

  function voteSym(kind: string) {
    switch (kind) {
      case "voted": return "✓";
      case "nil": return "✗";
      case "zero": return "0";
      default: return "·";
    }
  }
</script>

<div class="panel" style="display:flex; flex-direction:column; padding-bottom:0; min-height:0;">
  <div class="toolbar" style="padding-left:0; padding-right:0;">
    <h2 style="margin:0;">validators</h2>
    <input bind:value={search} placeholder="search moniker or address" />
    <span style="font-size:11px; color: var(--muted);">{rows.length}/{$validatorsOrdered.length}</span>
  </div>
  <div style="overflow:auto; flex:1; min-height:0;">
    <table>
      <thead>
        <tr>
          <th>#</th>
          <th on:click={() => setSort("moniker")}>moniker</th>
          <th on:click={() => setSort("power")} style="text-align:right;">vp%</th>
          <th>pv</th>
          <th>pc</th>
          <th>operator</th>
        </tr>
      </thead>
      <tbody>
        {#each rows as v (v.address)}
          <tr class:proposer={v.isProposer} class:our={v.address === $chain.chain?.ourValidator}>
            <td>{v.index + 1}</td>
            <td>
              {#if v.isProposer}<span title="proposer of this round" style="margin-right:4px;">👑</span>{/if}
              {v.moniker ?? v.address.slice(0,12)}{v.jailed ? " 🚫" : ""}
            </td>
            <td style="text-align:right;">{v.votingPowerPercent.toFixed(2)}</td>
            <td><span class="vote {v.prevote.kind}">{voteSym(v.prevote.kind)}</span></td>
            <td><span class="vote {v.precommit.kind}">{voteSym(v.precommit.kind)}</span></td>
            <td style="font-size:10px; color: var(--muted);">{(v.operatorAddress ?? "").slice(0,16)}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
</div>
