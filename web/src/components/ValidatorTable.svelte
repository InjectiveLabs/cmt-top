<script lang="ts">
  import { onMount } from "svelte";
  import { chain, validatorsOrdered, selectedValidator } from "../lib/stores";
  import { consensusSummary, explorerLink, shortHash } from "../lib/model";
  import type { Validator, Vote } from "../lib/types";
  let search = "",
    filter = "all",
    sort: "power" | "moniker" = "power",
    descending = true,
    watched = new Set<string>(),
    copyStatus = "";
  let lastSelected: string | null = null;
  $: if ($selectedValidator !== lastSelected) {
    copyStatus = "";
    lastSelected = $selectedValidator;
  }
  $: network = $chain.chain.network || "unknown";
  $: selected = $validatorsOrdered.find((v) => v.address === $selectedValidator);
  $: filtered = $validatorsOrdered.filter((v) => {
    const query = search.trim().toLowerCase();
    if (query && !`${v.moniker} ${v.address} ${v.operatorAddress}`.toLowerCase().includes(query))
      return false;
    return (
      filter === "all" ||
      (filter === "waiting" && v.precommit.kind === "absent") ||
      (filter === "nil" && (v.prevote.kind === "nil" || v.precommit.kind === "nil")) ||
      (filter === "proposer" && v.isProposer) ||
      (filter === "watched" && watched.has(`${network}:${v.address}`))
    );
  });
  $: rows = [...filtered].sort((a, b) => {
    const cmp =
      sort === "moniker"
        ? (a.moniker || a.address).localeCompare(b.moniker || b.address)
        : comparePower(a.votingPower, b.votingPower);
    return (descending ? -cmp : cmp) || a.address.localeCompare(b.address);
  });
  $: prevote = consensusSummary($validatorsOrdered, "prevote");
  $: precommit = consensusSummary($validatorsOrdered, "precommit");
  function comparePower(a: string, b: string) {
    try {
      return BigInt(a) === BigInt(b) ? 0 : BigInt(a) > BigInt(b) ? 1 : -1;
    } catch {
      return a.localeCompare(b);
    }
  }
  function setSort(value: "power" | "moniker") {
    if (sort === value) descending = !descending;
    else {
      sort = value;
      descending = value === "power";
    }
  }
  function toggleWatch(v: Validator) {
    const next = new Set(watched),
      key = `${network}:${v.address}`;
    if (next.has(key)) next.delete(key);
    else next.add(key);
    watched = next;
    try {
      localStorage.setItem("cmt-top-watchlist", JSON.stringify([...next]));
    } catch {
      /* Watchlist still works for this page. */
    }
  }
  function voteLabel(v: Vote, leading?: string) {
    return v.kind === "voted"
      ? leading && v.blockIDHash !== leading
        ? "Other block"
        : "Voted"
      : v.kind === "nil"
        ? "Nil"
        : v.kind === "zero"
          ? "Zero"
          : "Not observed";
  }
  async function copy(value: string) {
    try {
      await navigator.clipboard.writeText(value);
      copyStatus = "Copied to clipboard";
    } catch {
      copyStatus = "Copy unavailable. Select and copy the full value below.";
    }
  }
  function clearFilters() {
    search = "";
    filter = "all";
  }
  onMount(() => {
    try {
      const values: unknown = JSON.parse(localStorage.getItem("cmt-top-watchlist") || "[]");
      if (Array.isArray(values))
        watched = new Set(values.filter((v): v is string => typeof v === "string"));
    } catch {
      /* Ignore invalid saved preferences. */
    }
  });
</script>

<section class="panel validator-panel" aria-labelledby="validator-heading">
  <div class="panel-heading">
    <h2 id="validator-heading">Validators</h2>
    <span class="mono muted small">{rows.length} of {$validatorsOrdered.length}</span>
  </div>
  <div class="validator-toolbar">
    <label class="search-field" for="validator-search"
      ><span>Search validators</span>
      <div class="search-control">
        <input
          id="validator-search"
          type="search"
          bind:value={search}
          placeholder="Moniker or address"
        />{#if search}<button
            class="quiet"
            on:click={() => (search = "")}
            aria-label="Clear validator search">Clear</button
          >{/if}
      </div></label
    >
    <label for="validator-filter"
      ><span>Show</span><select id="validator-filter" bind:value={filter}
        ><option value="all">All validators</option><option value="waiting"
          >Precommit not observed</option
        ><option value="nil">Nil vote</option><option value="proposer">Proposer</option><option
          value="watched">Watchlist</option
        ></select
      ></label
    >
  </div>
  {#if selected}
    <section
      class="validator-detail"
      id="validator-detail"
      tabindex="-1"
      aria-label={`Details for ${selected.moniker || selected.address}`}
    >
      <div class="panel-line">
        <h3>{selected.moniker || shortHash(selected.address)}</h3>
        <button
          class="quiet"
          on:click={() => selectedValidator.set(null)}
          aria-label="Close validator details">Close</button
        >
      </div>
      <div class="detail-actions">
        <button
          on:click={() => toggleWatch(selected)}
          aria-pressed={watched.has(`${network}:${selected.address}`)}
          >{watched.has(`${network}:${selected.address}`)
            ? "★ Watched"
            : "☆ Watch validator"}</button
        >{#if explorerLink($chain.explorerURL, selected)}<a
            class="button-link"
            href={explorerLink($chain.explorerURL, selected) ?? undefined}
            target="_blank"
            rel="noopener noreferrer">Open explorer ↗</a
          >{/if}<span class="small muted"
          >{selected.votingPowerPercent.toFixed(2)}% voting power{selected.isProposer
            ? " · Current proposer"
            : ""}</span
        >
      </div>
      <dl class="detail-grid">
        <dt>Consensus address</dt>
        <dd>
          <div class="copy-line">
            <code>{selected.address}</code><button
              class="quiet"
              on:click={() => copy(selected.address)}
              aria-label="Copy consensus address">Copy</button
            >
          </div>
        </dd>
        {#if selected.operatorAddress}<dt>Operator address</dt>
          <dd>
            <div class="copy-line">
              <code>{selected.operatorAddress}</code><button
                class="quiet"
                on:click={() => copy(selected.operatorAddress ?? "")}
                aria-label="Copy operator address">Copy</button
              >
            </div>
          </dd>{/if}
        <dt>Prevote</dt>
        <dd><code>{selected.prevote.blockIDHash || voteLabel(selected.prevote)}</code></dd>
        <dt>Precommit</dt>
        <dd><code>{selected.precommit.blockIDHash || voteLabel(selected.precommit)}</code></dd>
        {#if selected.commissionRate}<dt>Commission</dt>
          <dd>{(Number(selected.commissionRate) * 100).toFixed(2)}%</dd>{/if}
        {#if selected.jailed}<dt>Staking status</dt>
          <dd class="warning-text">Jailed</dd>{/if}
      </dl>
      <span class="small muted" aria-live="polite">{copyStatus}</span>
    </section>
  {/if}
  <table class="validator-table">
    <caption class="sr-only"
      >Current round validator votes, weighted by voting power. Select a validator for full
      addresses and vote hashes.</caption
    >
    <colgroup
      ><col class="name-col" /><col class="power-col" /><col class="vote-col" /><col
        class="vote-col"
      /></colgroup
    >
    <thead
      ><tr
        ><th
          scope="col"
          aria-sort={sort === "moniker" ? (descending ? "descending" : "ascending") : "none"}
          ><button on:click={() => setSort("moniker")}
            >Validator {sort === "moniker" ? (descending ? "↓" : "↑") : ""}</button
          ></th
        ><th
          scope="col"
          class="numeric"
          aria-sort={sort === "power" ? (descending ? "descending" : "ascending") : "none"}
          ><button on:click={() => setSort("power")}
            >Power {sort === "power" ? (descending ? "↓" : "↑") : ""}</button
          ></th
        ><th scope="col"
          ><span class="full-label">Prevote</span><abbr class="compact-label" title="Prevote"
            >PV</abbr
          ></th
        ><th scope="col"
          ><span class="full-label">Precommit</span><abbr class="compact-label" title="Precommit"
            >PC</abbr
          ></th
        ></tr
      ></thead
    >
    <tbody>
      {#each rows as v (v.address)}
        <tr class:selected={v.address === $selectedValidator} class:proposer={v.isProposer}>
          <td
            ><button
              class="validator-name"
              on:click={() => {
                selectedValidator.set($selectedValidator === v.address ? null : v.address);
                copyStatus = "";
              }}
              aria-expanded={v.address === $selectedValidator}
              >{v.moniker || shortHash(v.address)}</button
            >
            <div class="row-tags">
              {#if v.isProposer}<span>Proposer</span
                >{/if}{#if watched.has(`${network}:${v.address}`)}<span>★ Watched</span
                >{/if}{#if v.jailed}<span class="warning-text">Jailed</span>{/if}
            </div></td
          >
          <td class="numeric mono">{v.votingPowerPercent.toFixed(2)}<span class="muted">%</span></td
          >
          <td
            class:positive={v.prevote.kind === "voted"}
            class:warning-text={v.prevote.kind === "nil" ||
              (v.prevote.kind === "voted" && v.prevote.blockIDHash !== prevote.leading?.hash)}
            ><span class:muted={v.prevote.kind === "absent"}
              >{voteLabel(v.prevote, prevote.leading?.hash)}</span
            ></td
          >
          <td
            class:positive={v.precommit.kind === "voted"}
            class:warning-text={v.precommit.kind === "nil" ||
              (v.precommit.kind === "voted" && v.precommit.blockIDHash !== precommit.leading?.hash)}
            ><span class:muted={v.precommit.kind === "absent"}
              >{voteLabel(v.precommit, precommit.leading?.hash)}</span
            ></td
          >
        </tr>
      {/each}
    </tbody>
  </table>
  {#if rows.length === 0}<div class="empty-state">
      <h3>{$validatorsOrdered.length ? "No validators match" : "Waiting for the validator set"}</h3>
      <p class="muted">
        {$validatorsOrdered.length
          ? "Try another moniker or address, or clear the filters."
          : "Validators appear after a successful RPC response."}
      </p>
      {#if $validatorsOrdered.length}<button on:click={clearFilters}
          >Clear search and filters</button
        >{/if}
    </div>{/if}
  <div class="panel-foot small muted">
    Nil is an explicit nil vote. Not observed means this RPC has not reported a vote for the current
    round.
  </div>
</section>
