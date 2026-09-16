<script lang="ts">
  import {
    hashColor,
    hashLabel,
    summarizePhase,
    type RecordedRound,
    type Phase,
  } from "../lib/rounds";
  import VoteSegments from "./VoteSegments.svelte";
  export let round: RecordedRound;
  export let phase: Phase;
  export let canonicalHash = "";
  export let onSelectHash: (hash: string) => void;
  let copied = "";
  let copiedRound = -1;
  $: if (round.round !== copiedRound) {
    copied = "";
    copiedRound = round.round;
  }
  $: label = phase === "prevote" ? "Prevotes" : "Precommits";
  $: summary = summarizePhase(round, phase);
  async function copy(hash: string) {
    try {
      await navigator.clipboard.writeText(hash);
      copied = "Full hash copied.";
    } catch {
      copied = "Copy unavailable. Select the full hash below to copy it.";
    }
  }
</script>

<section class="panel round-phase" aria-labelledby={`${phase}-cohort-heading`}>
  <div class="panel-heading">
    <h2 id={`${phase}-cohort-heading`}>{label}</h2>
    <span class="mono small">{summary.observedPercent.toFixed(2)}% observed</span>
  </div>
  <div class="panel-content">
    <VoteSegments {summary} {label} />
    <div class="panel-line small muted phase-observation">
      <span>{summary.observedCount} / {round.validators.length} validators observed</span><span
        >{summary.groups.filter((g) => g.hash).length} block hash{summary.groups.filter(
          (g) => g.hash,
        ).length === 1
          ? ""
          : "es"}</span
      >
    </div>
    {#if summary.hasQuorum}<p class="small positive">
        More than ⅔ of recorded voting power for one block hash.
      </p>{/if}
    {#if summary.conflicts.length}<p class="small warning-text">
        {summary.conflicts.length} validator{summary.conflicts.length === 1 ? " has" : "s have"} multiple
        hashes in this phase. Cohort percentages overlap; the bar counts each validator once.
      </p>{/if}
    {#if !round.validatorRosterComplete}<p class="small warning-text">
        Incomplete validator roster. Voting power totals may be incomplete.
      </p>{/if}
    <div class="phase-cohorts">
      {#each summary.groups as group (group.hash)}
        <details class="cohort" open={summary.groups.length < 4}>
          <summary
            ><span class="cohort-name"
              ><i style:background={hashColor(group.hash)}></i><span class="mono"
                >{hashLabel(group.hash)}</span
              >{#if group.hash && group.hash === canonicalHash}<span class="badge positive"
                  >Committed block</span
                >{/if}</span
            ><span class="mono">{group.percent.toFixed(2)}%</span></summary
          >
          <div class="cohort-body">
            {#if group.hash}<div class="copy-line">
                <code>{group.hash}</code><button
                  class="quiet"
                  on:click={() => copy(group.hash)}
                  aria-label={`Copy full ${phase} hash ${group.hash}`}>Copy</button
                >
              </div>
            {:else}<p class="small muted">
                An explicit nil vote; this is different from a vote not observed by this feed.
              </p>{/if}
            <div class="panel-line small cohort-count">
              <span
                >{group.members.length} validator{group.members.length === 1 ? "" : "s"} · {group.power.toString()}
                voting power</span
              ><button class="quiet" on:click={() => onSelectHash(group.hash)}
                >Filter comparison</button
              >
            </div>
            <ul class="cohort-members">
              {#each group.members as member (member.address)}<li>
                  <span class="break" title={member.address}
                    >{member.moniker || member.address}</span
                  ><span class="mono muted"
                    >{member.knownToRoster ? member.votingPower : "Unknown power"}</span
                  >
                </li>{/each}
            </ul>
          </div>
        </details>
      {/each}
      <details class="cohort missing-cohort">
        <summary
          ><span>Not observed</span><span class="mono">{summary.missingPercent.toFixed(2)}%</span
          ></summary
        >
        <div class="cohort-body">
          <p class="small muted">
            {summary.missing.length} validator{summary.missing.length === 1 ? " has" : "s have"} no {phase}
            in this feed. This can reflect timing or incomplete collection.
          </p>
          <ul class="cohort-members">
            {#each summary.missing as member (member.address)}<li>
                <span class="break" title={member.address}>{member.moniker || member.address}</span
                ><span class="mono muted">{member.votingPower}</span>
              </li>{/each}
          </ul>
        </div>
      </details>
    </div>
    {#if copied}<p class="small muted" role="status">{copied}</p>{/if}
  </div>
</section>
