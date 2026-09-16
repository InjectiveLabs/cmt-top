<script lang="ts">
  import { onMount, onDestroy, tick } from "svelte";
  import { fetchBlockRounds } from "../lib/api";
  import { createPoller } from "../lib/poll";
  import { dashboard, pausedAt } from "../lib/stores";
  import { elapsed, formatHeight, healthMode } from "../lib/model";
  import { parseHeight, roundsPath, shouldNavigate } from "../lib/routes";
  import {
    filterRoundValidators,
    hashLabel,
    normalizeInvestigation,
    percentOf,
    powerOf,
    selectRound,
    summarizePhase,
    voteIdentity,
    type BlockInvestigation,
    type ComparisonFilter,
  } from "../lib/rounds";
  import VoteSegments from "./VoteSegments.svelte";
  import RoundPhase from "./RoundPhase.svelte";
  import RecordedVote from "./RecordedVote.svelte";

  const statusLabels = {
    live: "Active consensus",
    committed: "Committed",
    passed: "Height passed",
    not_observed: "Not observed",
    evicted: "Outside retention",
  };
  export let height: number;
  export let token = "";
  export let now: number;
  export let onNavigate: (path: string) => void;
  export let onAuthFailure: () => void;
  let report: BlockInvestigation | null = null;
  let rawReport: unknown;
  let poller: ReturnType<typeof createPoller<unknown>> | undefined;
  let loading = false,
    error = "",
    loadedAt = 0;
  let enteredHeight = String(height),
    heightError = "";
  let selectedRound: number | null = null,
    followLatest = true,
    referenceRound = "";
  let search = "",
    filter: ComparisonFilter = "all",
    selectedHash: string | null = null;
  $: selectedRound = selectRound(report?.rounds ?? [], selectedRound, followLatest);
  $: round = report?.rounds.find((r) => r.round === selectedRound);
  $: if (
    referenceRound &&
    (referenceRound === String(selectedRound) ||
      !report?.rounds.some((r) => String(r.round) === referenceRound))
  )
    referenceRound = "";
  $: if (filter === "changed" && !reference) filter = "all";
  $: duration = observedDuration(report, $pausedAt || now);
  $: reference = report?.rounds.find((r) => String(r.round) === referenceRound);
  $: referenceValidators = new Map(reference?.validators.map((v) => [v.address, v]) ?? []);
  $: visibleValidators = round
    ? filterRoundValidators(round, reference, filter, search, selectedHash)
    : [];
  $: upstream = healthMode($dashboard.health, now);
  $: staleResponse = loadedAt > 0 && !$pausedAt && now - loadedAt > 5000;
  $: retainedHeights = report?.retention.retainedHeights ?? [];
  $: priorHeight = retainedHeights.filter((h) => h < height).sort((a, b) => b - a)[0];
  $: nextHeight = retainedHeights.filter((h) => h > height).sort((a, b) => a - b)[0];
  $: if (poller) poller.setPaused(Boolean($pausedAt));

  function navigate(event: MouseEvent, path: string) {
    if (!shouldNavigate(event)) return;
    event.preventDefault();
    onNavigate(path);
  }
  function goToHeight() {
    const parsed = parseHeight(enteredHeight.trim());
    if (!parsed) {
      heightError = "Enter a positive whole block height (up to 9,007,199,254,740,991).";
      return;
    }
    heightError = "";
    onNavigate(roundsPath(parsed));
  }
  function chooseRound(value: number) {
    followLatest = false;
    selectedRound = value;
  }
  async function filterHash(hash: string) {
    selectedHash = hash;
    filter = "all";
    search = "";
    await tick();
    const heading = document.getElementById("round-comparison-heading");
    heading?.focus({ preventScroll: true });
    heading?.scrollIntoView({ block: "start", behavior: "instant" });
  }
  function resetFilters() {
    search = "";
    filter = "all";
    selectedHash = null;
  }
  function observedDuration(report: BlockInvestigation | null, clock: number) {
    if (!report?.firstSeenAt) return "—";
    const end = report.status === "live" ? clock : Date.parse(report.lastSeenAt);
    const seconds = Math.max(0, Math.floor((end - Date.parse(report.firstSeenAt)) / 1000));
    if (!Number.isFinite(seconds)) return "—";
    return seconds < 60
      ? `${seconds}s`
      : seconds < 3600
        ? `${Math.floor(seconds / 60)}m ${seconds % 60}s`
        : `${Math.floor(seconds / 3600)}h ${Math.floor(seconds / 60) % 60}m`;
  }
  function exportEvidence() {
    if (!report) return;
    const exportData = {
      exportedAt: new Date().toISOString(),
      selectedRound,
      referenceRound: reference?.round ?? null,
      observationNotice:
        "Observed RPC vote events only. In-memory bounded retention; no historical backfill. Hash cohorts do not establish binary versions or nondeterminism. Conflicting observations need validation against signed votes.",
      investigation: rawReport,
    };
    const url = URL.createObjectURL(
      new Blob([JSON.stringify(exportData, null, 2)], { type: "application/json" }),
    );
    const link = document.createElement("a");
    link.href = url;
    link.download = `cmt-top-block-${height}-rounds.json`;
    link.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }
  export async function refreshForResume(): Promise<boolean> {
    return (await poller?.refresh(true)) ?? false;
  }
  onMount(() => {
    poller = createPoller({
      paused: Boolean($pausedAt),
      request: (signal) => fetchBlockRounds(height, token || undefined, signal),
      onData: (value) => {
        report = normalizeInvestigation(value, height);
        rawReport = value;
        loadedAt = Date.now();
        error = "";
      },
      onError: (failure) => {
        error =
          failure instanceof Error && failure.name !== "AbortError"
            ? failure.message
            : "The rounds request timed out. Retrying automatically.";
      },
      onLoading: (value) => (loading = value),
      onUnauthorized: onAuthFailure,
    });
  });
  onDestroy(() => poller?.dispose());
</script>

<div class="investigation-page">
  <nav class="investigation-nav" aria-label="Block investigation navigation">
    <a href="/" on:click={(event) => navigate(event, "/")}>← Dashboard</a>
    <div class="detail-actions">
      {#if priorHeight}<a
          href={roundsPath(priorHeight)}
          on:click={(event) => navigate(event, roundsPath(priorHeight))}
          >← Previous retained block</a
        >{/if}{#if nextHeight}<a
          href={roundsPath(nextHeight)}
          on:click={(event) => navigate(event, roundsPath(nextHeight))}>Next retained block →</a
        >{/if}
    </div>
  </nav>
  <div class="investigation-title">
    <div>
      <div class="eyebrow">Consensus investigation</div>
      <h1>Block <span class="mono">{formatHeight(height)}</span></h1>
      <p class="muted">
        Compare observed validator votes across rounds. This page stays pinned to this block.
      </p>
    </div>
    <button on:click={exportEvidence} disabled={!report}>Export evidence JSON</button>
  </div>
  <div class="investigation-toolbar">
    <form on:submit|preventDefault={goToHeight}>
      <label for="investigation-height">Block height</label>
      <div class="height-input">
        <input
          id="investigation-height"
          inputmode="numeric"
          autocomplete="off"
          bind:value={enteredHeight}
          aria-describedby={heightError ? "height-error" : undefined}
        /><button type="submit">Open block</button>
      </div>
    </form>
    {#if $dashboard.height > 0 && $dashboard.height !== height}<a
        href={roundsPath($dashboard.height)}
        on:click={(event) => navigate(event, roundsPath($dashboard.height))}
        >Open active block {formatHeight($dashboard.height)}</a
      >{/if}
    <div class="small muted investigation-freshness">
      <span
        >Rounds API: <strong class:warning-text={Boolean(error) || staleResponse}
          >{$pausedAt
            ? "View paused"
            : loadedAt
              ? `received ${elapsed(loadedAt, now)}`
              : "Connecting"}</strong
        ></span
      ><span
        >Upstream RPC: <strong class:warning-text={upstream !== "streaming"}>{upstream}</strong
        ></span
      >
    </div>
  </div>
  {#if heightError}<p id="height-error" class="error-text small" role="alert">{heightError}</p>{/if}
  {#if error}<div class="notice warning" role="alert">
      <span>{error} {report ? "Last received evidence remains visible." : ""}</span><button
        on:click={() => poller?.refresh()}
        disabled={loading || Boolean($pausedAt)}>Retry</button
      >
    </div>{/if}
  {#if !report}<section class="panel empty-state" role="status">
      <h2>{$pausedAt ? "Resume to load this block" : "Loading observed rounds…"}</h2>
      <p class="muted">The monitor keeps collecting while the page is paused.</p>
    </section>
  {:else}
    <section class="panel investigation-summary" aria-label="Block observation status">
      <div class="investigation-stats">
        <div>
          <span>Status</span><strong
            class:positive={report.status === "committed"}
            class:warning-text={report.status === "evicted"}>{statusLabels[report.status]}</strong
          >
        </div>
        <div><span>Observed for</span><strong class="mono">{duration}</strong></div>
        <div>
          <span>Rounds retained</span><strong class="mono"
            >{report.rounds.length}{#if report.coverage.roundsEvicted}<span class="small muted">
                · {report.coverage.roundsEvicted} evicted</span
              >{/if}</strong
          >
        </div>
        <div>
          <span>Latest vote / round observation</span><strong class="small"
            >{elapsed(report.lastSeenAt, $pausedAt || now)}</strong
          >
        </div>
      </div>
      {#if report.context.upgrade && report.context.upgrade.height === height}<p
          class="upgrade-context"
        >
          <span class="badge warning-text">Upgrade height</span>
          {report.context.upgrade.name}
        </p>{/if}
      <p class="small muted">
        Observation begins when this monitor receives events. Rounds are kept in memory, cleared on
        restart, and are not backfilled from the chain. Missing votes are not proof a validator did
        not vote.
      </p>
      {#if report.coverage.missingRounds || (report.coverage.firstObservedRound ?? 0) > 0 || !report.coverage.validatorRosterComplete || report.truncated}<p
          class="small warning-text coverage-warning"
        >
          Partial evidence. {#if (report.coverage.firstObservedRound ?? 0) > 0}First observed round: {report
              .coverage.firstObservedRound}.
          {/if}{#if report.coverage.missingRounds}{report.coverage.missingRounds} round{report
              .coverage.missingRounds === 1
              ? " was"
              : "s were"} not observed at this height.
          {/if}{#if !report.coverage.validatorRosterComplete}Validator roster is incomplete.
          {/if}{#if report.truncated}Some rounds or hash observations reached retention limits.{/if}
        </p>{/if}
      {#if report.retention.earliestHeight}<p class="small muted retention-copy">
          Retained heights {formatHeight(report.retention.earliestHeight)}–{formatHeight(
            report.retention.latestHeight,
          )} · Up to {report.retention.roundLimit} rounds per block.
        </p>{/if}
    </section>
    {#if !report.rounds.length}<section class="panel empty-state">
        <h2>
          {report.status === "evicted"
            ? "This block is outside retained history"
            : height > report.context.activeHeight
              ? "Waiting to observe this block"
              : "No round observations for this block"}
        </h2>
        <p class="muted">
          {report.status === "evicted"
            ? "Open a retained height or use an evidence file exported while this block was retained."
            : "Keep this page open to capture new events. A monitor started after this height cannot recover earlier round votes from the block API."}
        </p>
      </section>
    {:else}
      <section class="panel round-timeline" aria-labelledby="round-timeline-heading">
        <div class="panel-heading">
          <h2 id="round-timeline-heading">Observed rounds</h2>
          <label class="follow-round"
            ><input type="checkbox" bind:checked={followLatest} /> Follow latest round</label
          >
        </div>
        <div class="round-timeline-grid">
          {#each report.rounds as item (item.round)}{@const prevotes = summarizePhase(
              item,
              "prevote",
            )}{@const precommits = summarizePhase(item, "precommit")}<button
              class="round-selector"
              class:selected={selectedRound === item.round}
              aria-pressed={selectedRound === item.round}
              on:click={() => chooseRound(item.round)}
              aria-label={`Select round ${item.round}, prevotes ${prevotes.observedPercent.toFixed(1)}%, precommits ${precommits.observedPercent.toFixed(1)}% observed`}
              ><strong>Round {item.round}</strong>{#if item.truncated}<span
                  class="small warning-text">Partial</span
                >{/if}<span class="round-phase-label"
                >PV <span>{prevotes.observedPercent.toFixed(0)}%</span></span
              ><VoteSegments summary={prevotes} label={`Round ${item.round} prevotes`} /><span
                class="round-phase-label"
                >PC <span>{precommits.observedPercent.toFixed(0)}%</span></span
              ><VoteSegments
                summary={precommits}
                label={`Round ${item.round} precommits`}
              /></button
            >{/each}
        </div>
        <p class="panel-foot small muted">
          Bars show voting power. Colors follow exact hashes across phases and rounds. Each selected
          round uses its recorded validator roster.
        </p>
      </section>
      {#if selectedRound !== null && !round}<div class="notice warning" role="status">
          Round {selectedRound} is no longer retained. Select an available round or turn on Follow latest
          round.
        </div>{/if}
      {#if round}
        <div class="selected-round-heading">
          <div>
            <h2>Round {round.round} · hash cohorts</h2>
            <p class="small muted break">
              Proposer: {round.validators.find((v) => v.address === round.proposer)?.moniker ||
                (round.proposer ? "" : "Not observed")}
              {#if round.proposer}<code>{round.proposer}</code>{/if}
            </p>
            <p class="small muted">
              First seen {new Date(round.firstSeenAt).toLocaleString()} · updated {elapsed(
                round.lastSeenAt,
                $pausedAt || now,
              )}
            </p>
          </div>
          {#if !followLatest}<span class="badge">Round selection pinned</span>{/if}
        </div>
        <div class="round-phase-grid">
          <RoundPhase
            {round}
            phase="prevote"
            canonicalHash={report.canonicalHash}
            onSelectHash={filterHash}
          /><RoundPhase
            {round}
            phase="precommit"
            canonicalHash={report.canonicalHash}
            onSelectHash={filterHash}
          />
        </div>
        <section class="panel round-comparison" aria-labelledby="round-comparison-heading">
          <div class="panel-heading">
            <h2 id="round-comparison-heading" tabindex="-1">Validator comparison</h2>
            <span class="small muted"
              >{visibleValidators.length} / {round.validators.length} validators</span
            >
          </div>
          <div class="validator-toolbar">
            <label class="search-field"
              ><span>Search validator</span><input
                bind:value={search}
                type="search"
                placeholder="Name or consensus address"
              /></label
            ><label
              ><span>Show</span><select bind:value={filter}
                ><option value="all">All observations</option><option value="phase_change"
                  >Different prevote / precommit</option
                ><option value="other_block">Other than leading block hash</option><option
                  value="nil">Any nil vote</option
                ><option value="missing">Any vote not observed</option><option value="conflicting"
                  >Conflicting observations</option
                ><option value="changed" disabled={!reference}>Changed from reference round</option
                ></select
              ></label
            ><label
              ><span>Compare with round</span><select bind:value={referenceRound}
                ><option value="">No reference</option
                >{#each report.rounds.filter((r) => r.round !== round.round) as r (r.round)}<option
                    value={String(r.round)}>Round {r.round}</option
                  >{/each}</select
              ></label
            >
          </div>
          {#if selectedHash !== null}<div class="hash-filter small">
              <span>Hash filter: <code>{hashLabel(selectedHash)}</code> in either phase</span
              ><button class="quiet" on:click={() => (selectedHash = null)}
                >Clear hash filter</button
              >
            </div>{/if}
          <p class="comparison-explanation small muted">
            Different votes between phases or rounds can be normal consensus behavior. “Other” means
            different from the leading observed block hash in that phase, not an invalid vote.
            {#if reference}Only validators in the selected round's recorded roster are shown.{/if}
          </p>
          {#if !visibleValidators.length}<div class="empty-state">
              <h3>No validators match these filters</h3>
              <p class="muted">Try a different name, observation filter, or hash cohort.</p>
              <button on:click={resetFilters}>Clear filters</button>
            </div>
          {:else}<table class="round-comparison-table">
              <thead
                ><tr
                  ><th scope="col">Validator</th><th scope="col">Voting power</th><th scope="col"
                    >Prevote · R{round.round}</th
                  ><th scope="col">Precommit · R{round.round}</th></tr
                ></thead
              ><tbody
                >{#each visibleValidators as validator (validator.address)}{@const ref =
                    referenceValidators.get(validator.address)}<tr
                    ><td data-label="Validator"
                      ><details>
                        <summary class="break">{validator.moniker || validator.address}</summary
                        ><code>{validator.address}</code>{#if !validator.knownToRoster}<p
                            class="small warning-text"
                          >
                            Voter is not in the recorded roster.
                          </p>{/if}
                      </details></td
                    ><td data-label="Voting power"
                      >{#if validator.knownToRoster}<span class="mono"
                          >{percentOf(
                            powerOf(validator.votingPower),
                            powerOf(round.totalVotingPower),
                          ).toFixed(2)}%</span
                        >
                        <div class="small muted mono">{validator.votingPower}</div>{:else}<span
                          class="muted">Unknown power</span
                        >{/if}</td
                    ><td data-label={`Prevote · R${round.round}`}
                      ><RecordedVote vote={validator.prevote} />{#if reference}<div
                          class="reference-vote"
                          class:changed={ref &&
                            voteIdentity(ref.prevote) !== voteIdentity(validator.prevote)}
                        >
                          <span class="small muted"
                            >R{reference.round}
                            {ref && voteIdentity(ref.prevote) !== voteIdentity(validator.prevote)
                              ? "· changed"
                              : ""}</span
                          >{#if ref}<RecordedVote vote={ref.prevote} />{:else}<span
                              class="small muted">Not in reference roster</span
                            >{/if}
                        </div>{/if}</td
                    ><td data-label={`Precommit · R${round.round}`}
                      ><RecordedVote vote={validator.precommit} />{#if reference}<div
                          class="reference-vote"
                          class:changed={ref &&
                            voteIdentity(ref.precommit) !== voteIdentity(validator.precommit)}
                        >
                          <span class="small muted"
                            >R{reference.round}
                            {ref &&
                            voteIdentity(ref.precommit) !== voteIdentity(validator.precommit)
                              ? "· changed"
                              : ""}</span
                          >{#if ref}<RecordedVote vote={ref.precommit} />{:else}<span
                              class="small muted">Not in reference roster</span
                            >{/if}
                        </div>{/if}</td
                    ></tr
                  >{/each}</tbody
              >
            </table>{/if}
        </section>
      {/if}
    {/if}
    <details class="panel investigation-guide" open>
      <summary>How to confirm an upgrade or execution mismatch</summary>
      <div>
        <p>
          Use these cohorts to identify which validator operators to contact. Vote hashes alone do
          not prove different binaries or nondeterministic execution.
        </p>
        <ol>
          <li>
            Compare the application binary version, build commit, and executable checksum with each
            operator in the relevant cohorts. The connected RPC's CometBFT version is not the
            validator application's binary version.
          </li>
          <li>
            Match the exact height and round with proposal rejection, application, and upgrade logs.
            Preserve signed vote evidence when a validator has conflicting observations in the same
            phase and round.
          </li>
          <li>
            Compare AppHash at the same committed height and chain across independent RPC nodes. A
            stuck, uncommitted block has no committed AppHash to compare. An AppHash mismatch
            indicates differing reported state, but still needs investigation to establish its
            cause.
          </li>
        </ol>
        <p class="small muted">
          Nil votes, missing observations, and hash changes across rounds can result from timing,
          locking, or incomplete collection. <a
            href="https://github.com/cometbft/cometbft/blob/v0.38.21/spec/consensus/consensus.md"
            target="_blank"
            rel="noreferrer">CometBFT consensus specification ↗</a
          >
        </p>
      </div>
    </details>
  {/if}
</div>
