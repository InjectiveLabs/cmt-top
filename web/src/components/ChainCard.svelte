<script lang="ts">
  import { chain, validatorsOrdered } from "../lib/stores";
  import { consensusSummary, formatHeight, shortHash, stepName } from "../lib/model";
  $: summaries = [
    { title: "Prevote", value: consensusSummary($validatorsOrdered, "prevote") },
    { title: "Precommit", value: consensusSummary($validatorsOrdered, "precommit") },
  ];
</script>

<section class="chain-summary" aria-label="Chain consensus summary">
  <div class="metric">
    <span>Consensus height</span><strong class="mono">{formatHeight($chain.height)}</strong>
  </div>
  <div class="metric">
    <span>Committed height</span><strong class="mono">{formatHeight($chain.committedHeight)}</strong
    >
  </div>
  <div class="metric">
    <span>Round / step</span><strong class="step"
      ><span class="mono">{$chain.round}</span> <span class="muted">/</span>
      {stepName($chain.step)}</strong
    >
  </div>
  <div class="metric">
    <span>Average block time</span><strong class="mono"
      >{$chain.blockTime > 0 ? `${($chain.blockTime / 1000).toFixed(2)}s` : "—"}</strong
    >
  </div>
</section>
{#if $chain.upgrade}<div class="notice warning">
    <span
      >Upgrade <strong>{$chain.upgrade.name}</strong> at
      <span class="mono">{formatHeight($chain.upgrade.height)}</span>
      · {Math.max(0, $chain.upgrade.height - $chain.committedHeight).toLocaleString()} blocks remaining</span
    >
  </div>{/if}
<section class="consensus-grid" aria-label="Consensus voting power">
  {#each summaries as { title, value }}
    <div class="panel consensus-panel">
      <div class="panel-line">
        <h2>{title}</h2>
        <strong class="mono"
          >{value.leading?.percent.toFixed(1) ?? "0.0"}%
          <span class="muted small">leading BlockID</span></strong
        >
      </div>
      <div
        class="power-meter"
        role="img"
        aria-label={`${title}: ${value.leading?.percent.toFixed(1) ?? 0}% for the leading BlockID, ${value.nilPct.toFixed(1)}% nil, ${value.absentPct.toFixed(1)}% not observed. Reference line at two thirds.`}
      >
        {#each value.groups as group, i}<span
            class:leading={i === 0}
            class:alternate={i > 0}
            style={`width:${group.percent}%`}
          ></span>{/each}
        <span class="nil" style={`width:${value.nilPct}%`}></span>
      </div>
      <div class="legend">
        <span><i class="leading"></i>Leading {value.leading?.percent.toFixed(1) ?? "0.0"}%</span
        >{#if value.groups.length > 1}<span
            ><i class="alternate"></i>Other blocks {value.groups
              .slice(1)
              .reduce((n, g) => n + g.percent, 0)
              .toFixed(1)}%</span
          >{/if}<span><i class="nil"></i>Nil {value.nilPct.toFixed(1)}%</span><span
          >Not observed {value.absentPct.toFixed(1)}%</span
        >
      </div>
      <div class="consensus-context">
        <span class="mono"
          >{value.leading ? shortHash(value.leading.hash) : "No BlockID observed"}</span
        ><span>{value.observedCount}/{value.totalCount} validators observed</span>
      </div>
      <div class="small muted">
        {value.hasQuorum
          ? "More than ⅔ observed for the leading BlockID"
          : "│ ⅔ voting-power reference"}
      </div>
    </div>
  {/each}
</section>
