<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { createWS } from "./lib/ws";
  import { fetchSnapshot } from "./lib/api";
  import {
    applySnapshot, applyVote, applyChain, applyDivergenceLive,
    applyDivergenceResolved, applyDivergenceSnapshot, applyBlock,
    resetRoundVotes, applyProposer,
    chain, validatorsOrdered, divergence, errors
  } from "./lib/stores";
  import Header from "./components/Header.svelte";
  import ChainCard from "./components/ChainCard.svelte";
  import ValidatorTable from "./components/ValidatorTable.svelte";
  import DivergencePanel from "./components/DivergencePanel.svelte";

  const token = new URLSearchParams(location.search).get("token") ?? undefined;

  let ws: ReturnType<typeof createWS>;
  let unsubMessages: () => void;

  onMount(async () => {
    try {
      const snap = await fetchSnapshot(token);
      applySnapshot(snap);
    } catch (err) {
      console.warn("snapshot failed", err);
    }
    ws = createWS("/ws", token);
    unsubMessages = ws.onMessage((env) => {
      switch (env.type) {
        case "state.snapshot":
          applySnapshot(env.payload);
          break;
        case "vote.received":
          applyVote(env.payload);
          break;
        case "round.changed":
          // Server already cleared per-validator votes for the new round;
          // mirror it client-side and mark the new proposer.
          resetRoundVotes();
          applyChain(env.payload);
          if (env.payload?.Proposer) applyProposer(env.payload.Proposer);
          break;
        case "block.committed":
          // Block just committed; new round 0 starts. Clear votes and mark
          // the proposer of the committed block — Injective's NewRound is
          // sporadic so this is the reliable proposer source.
          resetRoundVotes();
          applyBlock(env.payload);
          applyChain(env.payload);
          if (env.payload?.ProposerAddr) applyProposer(env.payload.ProposerAddr);
          break;
        case "divergence.detected":
          applyDivergenceLive(env.payload);
          break;
        case "divergence.resolved":
          applyDivergenceResolved(env.payload);
          break;
        case "divergence.snapshot":
          applyDivergenceSnapshot(env.payload);
          break;
      }
    });
  });

  onDestroy(() => {
    if (unsubMessages) unsubMessages();
    if (ws) ws.close();
  });
</script>

<div class="app">
  <Header status={ws?.status} />
  <div class="left">
    <ChainCard />
  </div>
  <div class="center">
    <ValidatorTable />
  </div>
  <div class="right">
    <DivergencePanel />
  </div>
</div>
