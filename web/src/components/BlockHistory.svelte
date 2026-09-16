<script lang="ts">
  import { blocks } from "../lib/stores";
  import { formatHeight } from "../lib/model";
  $: samples = $blocks.filter((b) => typeof b.blockTimeMs === "number" && b.blockTimeMs > 0);
  $: max = Math.max(1, ...samples.map((b) => b.blockTimeMs ?? 0));
  $: points = samples
    .map(
      (b, i) =>
        `${10 + (i / Math.max(1, samples.length - 1)) * 300},${70 - ((b.blockTimeMs ?? 0) / max) * 60}`,
    )
    .join(" ");
  $: latest = $blocks[$blocks.length - 1];
</script>

<section class="panel block-panel" aria-labelledby="block-heading">
  <div class="panel-heading">
    <h2 id="block-heading">Recent block intervals</h2>
    <span class="small muted">{samples.length} samples</span>
  </div>
  <div class="panel-content">
    {#if samples.length > 1}<div class="small muted">
        Max <span class="mono">{(max / 1000).toFixed(2)}s</span>
      </div>
      <svg
        class="block-chart"
        viewBox="0 0 320 80"
        role="img"
        aria-label={`Observed block intervals from height ${samples[0].height} to ${samples[samples.length - 1].height}, maximum ${(max / 1000).toFixed(2)} seconds.`}
        ><line x1="10" y1="70" x2="310" y2="70" class="chart-axis" /><polyline
          {points}
          class="chart-line"
        /></svg
      >
      <div class="chart-labels small mono muted">
        <span>{formatHeight(samples[0].height)}</span><span
          >{formatHeight(samples[samples.length - 1].height)}</span
        >
      </div>
    {:else}<p class="small muted">
        Collecting observed block intervals. A trend appears after two samples.
      </p>{/if}
    {#if latest}<details class="latest-block">
        <summary class="small"
          >Latest recorded block <span class="mono">{formatHeight(latest.height)}</span></summary
        >
        <dl class="detail-grid">
          <dt>Time</dt>
          <dd>{new Date(latest.time).toLocaleString()}</dd>
          <dt>Transactions</dt>
          <dd>{latest.numTxs ?? "Not provided"}</dd>
          {#if latest.blockIDHash}<dt>BlockID</dt>
            <dd><code>{latest.blockIDHash}</code></dd>{/if}{#if latest.appHash}<dt>AppHash</dt>
            <dd><code>{latest.appHash}</code></dd>{/if}
        </dl>
      </details>{/if}
  </div>
</section>
