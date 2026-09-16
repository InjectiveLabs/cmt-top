<script lang="ts">
  import { chain } from "../lib/stores";
  import { elapsed, formatHeight } from "../lib/model";
  export let now: number;
  $: comparison = $chain.rpcComparison;
  const statusLabels = {
    not_configured: "Not configured",
    matching: "Matching at checked height",
    mismatch: "AppHash mismatch",
    incomplete: "Comparison incomplete",
  };
  const endpointLabels = {
    reference: "Reference",
    match: "Matches",
    mismatch: "Mismatch",
    lagging: "Behind checked height",
    chain_mismatch: "Different chain",
    error: "Unavailable",
  };
</script>

<section class="panel" aria-labelledby="rpc-heading">
  <div class="panel-heading"><h2 id="rpc-heading">RPC AppHash comparison</h2></div>
  <div class="panel-content">
    <p
      class:positive={comparison.status === "matching"}
      class:warning-text={comparison.status === "mismatch" || comparison.status === "incomplete"}
    >
      {statusLabels[comparison.status]}
    </p>
    {#if comparison.status === "not_configured"}<p class="small muted">
        Add comparison endpoints with <code>--monitored-rpc</code> in the server configuration.
      </p>
    {:else}<div class="small muted">
        Height <span class="mono">{formatHeight(comparison.height)}</span> · Checked {elapsed(
          comparison.checkedAt,
          now,
        )}
      </div>
      {#each comparison.endpoints as endpoint}<details class="endpoint">
          <summary
            ><span class="endpoint-name">{endpoint.endpoint}</span><span
              class="badge"
              class:warning-text={endpoint.status !== "match" && endpoint.status !== "reference"}
              >{endpointLabels[endpoint.status]}</span
            ></summary
          >
          <dl class="detail-grid">
            <dt>Chain</dt>
            <dd>{endpoint.chainId || "Unknown"}</dd>
            <dt>Latest height</dt>
            <dd class="mono">{formatHeight(endpoint.latestHeight)}</dd>
            <dt>Checked height</dt>
            <dd class="mono">{formatHeight(endpoint.height)}</dd>
            <dt>AppHash</dt>
            <dd><code>{endpoint.appHash || "Not available"}</code></dd>
            {#if endpoint.error}<dt>Diagnostic</dt>
              <dd class="break">{endpoint.error}</dd>{/if}
          </dl>
        </details>{/each}
    {/if}
  </div>
</section>
