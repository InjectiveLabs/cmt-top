<script lang="ts">
  import { dashboard, pausedAt } from "../lib/stores";
  import { elapsed, healthMode } from "../lib/model";
  import type { ConnectionStatus } from "../lib/ws";
  export let feedStatus: ConnectionStatus;
  export let lastMessageAt: number;
  export let now: number;
  export let loading = false;
  export let resuming = false;
  export let pausing = false;
  export let cadenceMs = 1000;
  export let onTogglePause: () => void;
  $: mode = healthMode($dashboard.health, now);
  $: feedQuiet =
    feedStatus === "open" &&
    lastMessageAt > 0 &&
    now - lastMessageAt > Math.max(5000, cadenceMs * 3);
  $: feedLabel = feedQuiet
    ? "No recent data"
    : {
        open: "Connected",
        connecting: "Connecting",
        reconnecting: "Reconnecting",
        resyncing: "Syncing",
        closed: "Disconnected",
        suspended: "Background tab",
      }[feedStatus];
</script>

<header class="app-header">
  <div class="brand">
    <a href="/" aria-label="cmt-top dashboard">cmt<span>-top</span></a><span class="network mono"
      >{$dashboard.chain.network || "Awaiting chain"}</span
    >{#if $dashboard.displayName}<span class="small muted">{$dashboard.displayName}</span>{/if}
  </div>
  <div class="header-right">
    <div class="health-strip">
      <span
        >Browser feed <strong
          class:positive={feedStatus === "open" && !feedQuiet}
          class:warning-text={feedStatus !== "open" || feedQuiet}>{feedLabel}</strong
        ></span
      >
      <span
        >RPC <strong class:positive={mode === "streaming"} class:warning-text={mode !== "streaming"}
          >{loading && !$dashboard.receivedAt ? "Connecting" : mode}</strong
        ></span
      >
      <span
        >Data age <strong class="mono">{elapsed($dashboard.health.lastSuccessAt, now)}</strong
        ></span
      >
    </div>
    <button
      class="pause-button"
      on:click={onTogglePause}
      disabled={!$dashboard.receivedAt || resuming || pausing}
      aria-pressed={Boolean($pausedAt)}
      >{pausing
        ? "Capturing evidence…"
        : resuming
          ? "Refreshing…"
          : $pausedAt
            ? "Resume"
            : "Pause view"}</button
    >
  </div>
</header>
