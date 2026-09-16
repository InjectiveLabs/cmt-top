<script lang="ts">
  import { hashColor, hashLabel, type RecordedVote } from "../lib/rounds";
  export let vote: RecordedVote | undefined;
</script>

<div class="recorded-vote">
  {#if !vote?.observed}<span class="muted">Not observed</span>
  {:else}
    {#each vote.hashes as item (item.hash)}
      <span class="vote-hash" title={item.hash || "Explicit nil vote"}
        ><i style:background={hashColor(item.hash)}></i><span class="mono"
          >{hashLabel(item.hash)}</span
        ></span
      >
    {/each}
    {#if vote.conflicting}<span class="warning-text small">Conflicting observations</span>{/if}
    {#if vote.truncated}<span class="warning-text small">Hash retention limit reached</span>{/if}
  {/if}
</div>
