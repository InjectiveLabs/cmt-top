<script lang="ts">
  import { tick } from "svelte";
  import { divergence, chain, selectedValidator } from "../lib/stores";
  import { formatHeight, healthMode, shortHash } from "../lib/model";
  import { roundKey } from "../lib/stores";
  export let now: number;
  $: live = $divergence.live.filter((r) => r.IsDivergent);
  $: history = $divergence.history.filter((r) => r.IsDivergent);
  $: mode = healthMode($chain.health, now);
  function name(address: string) {
    return (
      $chain.validators.find((v) => v.address === address.toUpperCase())?.moniker ||
      shortHash(address)
    );
  }
  async function inspect(address: string) {
    selectedValidator.set(address.toUpperCase());
    await tick();
    const panel = document.getElementById("validator-detail");
    panel?.scrollIntoView({ block: "center" });
    panel?.focus({ preventScroll: true });
  }
</script>

<section class="panel incident-panel" aria-labelledby="split-heading">
  <div class="panel-heading">
    <h2 id="split-heading">Vote splits</h2>
    <span class="badge" class:warning-text={live.length > 0}>{live.length} active</span>
  </div>
  {#if !live.length}<div class="panel-content small muted">
      {mode === "unavailable"
        ? "Feed unavailable. Waiting for received votes."
        : mode === "stale"
          ? "Feed is stale. No current split assessment."
          : !$divergence.live.length
            ? "Waiting for votes in the current round."
            : "No split observed in the received votes."}
    </div>{/if}
  {#each [...live, ...history] as r (roundKey(r))}
    <details class="incident" open={!r.Resolved}>
      <summary
        ><span class="incident-id"
          ><span class="mono">{formatHeight(r.Height)} / r{r.Round}</span><span class="small muted"
            >{r.Type === 1 ? "Prevote" : "Precommit"}</span
          ></span
        ><span class="badge" class:warning-text={!r.Resolved}
          >{r.Resolved ? "Resolved" : "Split observed"}</span
        ></summary
      >
      <div class="incident-body">
        <p class="small muted">
          Observed power {r.TotalVotingPower > 0
            ? ((r.TotalVotedPower / r.TotalVotingPower) * 100).toFixed(1)
            : "0.0"}% · Not observed {r.TotalVotingPower > 0
            ? Math.max(0, 100 - (r.TotalVotedPower / r.TotalVotingPower) * 100).toFixed(1)
            : "100.0"}%
        </p>
        {#each r.Groups as group}
          <details class="vote-group">
            <summary
              ><span
                ><span
                  class="group-marker"
                  class:canonical={group.IsCanonical}
                  class:leading={!group.IsCanonical &&
                    group.BlockIDHash === r.Groups.find((g) => g.BlockIDHash)?.BlockIDHash}
                  class:alternate={group.BlockIDHash &&
                    !group.IsCanonical &&
                    group.BlockIDHash !== r.Groups.find((g) => g.BlockIDHash)?.BlockIDHash}
                ></span>{!group.BlockIDHash
                  ? "Nil"
                  : group.IsCanonical
                    ? "Canonical"
                    : group.BlockIDHash === r.Groups.filter((g) => g.BlockIDHash)[0]?.BlockIDHash
                      ? "Leading"
                      : "Other BlockID"}</span
              ><span class="mono">{group.VotingPowerPct.toFixed(1)}%</span></summary
            >
            <div class="group-detail">
              <code>{group.BlockIDHash || "Explicit nil votes"}</code>
              <p class="small muted">
                {group.ValidatorCount} validator{group.ValidatorCount === 1 ? "" : "s"}
              </p>
              <div class="member-list">
                {#each group.Validators as address}<button
                    class="quiet"
                    on:click={() => inspect(address)}>{name(address)}</button
                  >{/each}
              </div>
            </div>
          </details>
        {/each}
      </div>
    </details>
  {/each}
  <div class="panel-foot small muted">
    BlockID vote differences are separate from application-state hash comparisons.
  </div>
</section>
