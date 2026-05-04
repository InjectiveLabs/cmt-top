<script lang="ts">
  import { divergence, type DivergenceGroup, type DivergenceRound } from "../lib/stores";

  function colorFor(hash: string): string {
    if (!hash) return "#6b7280";
    let h = 0;
    for (let i = 0; i < hash.length; i++) h = (h * 31 + hash.charCodeAt(i)) | 0;
    const hue = Math.abs(h) % 360;
    return `hsl(${hue}, 60%, 50%)`;
  }
  function labelFor(hash: string): string {
    if (!hash) return "<nil>";
    return hash.slice(0, 10);
  }
  function typeName(t: number): string {
    return t === 1 ? "prevote" : t === 2 ? "precommit" : String(t);
  }
</script>

<div class="panel">
  <h2>apphash divergence</h2>
  {#if $divergence.live.length === 0 && $divergence.history.length === 0}
    <div style="color: var(--muted); font-size: 11px;">(waiting for votes…)</div>
  {/if}

  {#each $divergence.live as r (`${r.Height}/${r.Round}/${r.Type}`)}
    <div class="divergence-card">
      <div class="header">
        <span>h{r.Height} r{r.Round} {typeName(r.Type)}</span>
        <span class="pill {r.IsDivergent ? 'divergent' : 'canonical'}">
          {r.IsDivergent ? "divergent" : "ok"}
        </span>
      </div>
      <div class="bar" style="margin-top:4px;">
        {#each r.Groups as g (g.BlockIDHash)}
          <span style="width: {g.VotingPowerPct}%; background: {g.IsCanonical ? 'var(--canonical)' : (r.IsDivergent && g !== r.Groups[0] && g.BlockIDHash !== '' ? 'var(--divergent)' : colorFor(g.BlockIDHash))};" title={`${labelFor(g.BlockIDHash)} — ${g.VotingPowerPct.toFixed(1)}% (${g.ValidatorCount} validators)`}></span>
        {/each}
      </div>
      {#each r.Groups as g}
        <div class="group">
          <span style="color: {colorFor(g.BlockIDHash)}; font-family: ui-monospace,monospace;">{labelFor(g.BlockIDHash)}</span>
          <span class="bar" style="height:6px;"><span style="width: {g.VotingPowerPct}%; background: {colorFor(g.BlockIDHash)};"></span></span>
          <span>{g.VotingPowerPct.toFixed(1)}% · {g.ValidatorCount}</span>
        </div>
      {/each}
    </div>
  {/each}

  {#if $divergence.history.length > 0}
    <h2 style="margin-top:12px;">recent divergent</h2>
    {#each $divergence.history.slice(0, 8) as r (`${r.Height}/${r.Round}/${r.Type}`)}
      <div class="divergence-card" style="opacity: 0.85;">
        <div class="header">
          <span>h{r.Height} r{r.Round} {typeName(r.Type)}</span>
          <span class="pill canonical">{labelFor(r.CanonicalHash)}</span>
        </div>
        <div class="bar" style="margin-top:4px;">
          {#each r.Groups as g}
            <span style="width: {g.VotingPowerPct}%; background: {g.IsCanonical ? 'var(--canonical)' : 'var(--divergent)'};"></span>
          {/each}
        </div>
      </div>
    {/each}
  {/if}
</div>
