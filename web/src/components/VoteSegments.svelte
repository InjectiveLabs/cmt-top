<script lang="ts">
  import type { PhaseSummary } from "../lib/rounds";
  export let summary: PhaseSummary;
  export let label: string;
</script>

<div
  class="round-power-meter"
  role="img"
  aria-label={`${label}: ${summary.observedPercent.toFixed(1)}% voting power observed; ${summary.missingPercent.toFixed(1)}% not observed.`}
>
  {#each summary.segments as segment (segment.key)}
    <span
      style:width={`${segment.percent}%`}
      style:background={segment.color}
      title={`${segment.label}: ${segment.percent.toFixed(2)}%`}
      class:unobserved={segment.key === "missing"}
    ></span>
  {/each}
</div>
