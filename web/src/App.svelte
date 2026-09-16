<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { createWS, type ConnectionStatus, type WSClient } from "./lib/ws";
  import { APIError, fetchSnapshot } from "./lib/api";
  import {
    applyEnvelope,
    applySnapshot,
    dashboard,
    pausedAt,
    pauseView,
    resumeView,
  } from "./lib/stores";
  import { formatHeight, healthMode } from "./lib/model";
  import Header from "./components/Header.svelte";
  import ChainCard from "./components/ChainCard.svelte";
  import ValidatorTable from "./components/ValidatorTable.svelte";
  import DivergencePanel from "./components/DivergencePanel.svelte";
  import RPCPanel from "./components/RPCPanel.svelte";
  import BlockHistory from "./components/BlockHistory.svelte";
  import BlockRounds from "./components/BlockRounds.svelte";
  import { parseRoute, roundsPath, shouldNavigate, type AppRoute } from "./lib/routes";

  let route: AppRoute =
    typeof location === "undefined" ? { page: "dashboard" } : parseRoute(location.pathname);
  let investigationPage: BlockRounds | undefined;
  function navigate(path: string) {
    if (location.pathname !== path) history.pushState(null, "", path);
    route = parseRoute(path);
    window.scrollTo(0, 0);
  }
  function navigateLink(event: MouseEvent, path: string) {
    if (!shouldNavigate(event)) return;
    event.preventDefault();
    navigate(path);
  }

  let token = "",
    enteredToken = "",
    requiresAuth = false,
    loading = true,
    error = "",
    resuming = false;
  let feedStatus: ConnectionStatus = "connecting",
    lastMessageAt = 0,
    now = Date.now();
  let ws: WSClient | undefined,
    unsubscribers: (() => void)[] = [],
    timer: ReturnType<typeof setInterval>;
  let generation = 0,
    destroyed = false;
  $: mode = healthMode($dashboard.health, now);
  function rememberToken(value: string) {
    try {
      if (value) sessionStorage.setItem("cmt-top-token", value);
      else sessionStorage.removeItem("cmt-top-token");
    } catch {
      /* Session storage can be disabled. */
    }
  }
  function stopSocket() {
    unsubscribers.forEach((fn) => fn());
    unsubscribers = [];
    ws?.close();
    ws = undefined;
  }
  function authFailed() {
    generation++;
    requiresAuth = true;
    loading = false;
    error = "Enter a valid access token to connect.";
    rememberToken("");
    stopSocket();
    feedStatus = "closed";
  }
  async function connect() {
    const run = ++generation;
    stopSocket();
    loading = true;
    error = "";
    requiresAuth = false;
    feedStatus = "connecting";
    try {
      const snapshot = await fetchSnapshot(token || undefined, AbortSignal.timeout(10000));
      if (destroyed || run !== generation) return;
      applySnapshot(snapshot);
      rememberToken(token);
      loading = false;
      ws = createWS("/ws", token || undefined, authFailed);
      unsubscribers = [
        ws.status.subscribe((value) => (feedStatus = value)),
        ws.lastMessageAt.subscribe((value) => (lastMessageAt = value)),
        ws.onMessage((env) => applyEnvelope(env)),
      ];
    } catch (e) {
      if (destroyed || run !== generation) return;
      loading = false;
      feedStatus = "closed";
      if (e instanceof APIError && e.status === 401) authFailed();
      else
        error =
          e instanceof APIError
            ? e.message
            : "Could not reach the dashboard. Check the server and try again.";
    }
  }
  async function authenticate() {
    token = enteredToken.trim();
    enteredToken = "";
    await connect();
  }
  function openNodeDetails() {
    const details = document.getElementById("node-details");
    if (details instanceof HTMLDetailsElement) details.open = true;
  }
  async function togglePause() {
    if (!$pausedAt) {
      pauseView();
      return;
    }
    resuming = true;
    try {
      const snapshot = await fetchSnapshot(token || undefined, AbortSignal.timeout(10000));
      if (route.page === "rounds" && !(await investigationPage?.refreshForResume())) {
        throw new Error("Could not refresh block investigation.");
      }
      applySnapshot(snapshot);
      resumeView();
      ws?.send({ type: "resync" });
      error = "";
    } catch (e) {
      if (e instanceof APIError && e.status === 401) authFailed();
      else error = "Could not refresh the paused view. Retry when the dashboard is available.";
    } finally {
      resuming = false;
    }
  }
  onMount(() => {
    const url = new URL(location.href),
      urlToken = url.searchParams.get("token");
    if (urlToken !== null) {
      token = urlToken;
      url.searchParams.delete("token");
      history.replaceState(null, "", url.pathname + url.search + url.hash);
    } else {
      try {
        token = sessionStorage.getItem("cmt-top-token") ?? "";
      } catch {
        /* Optional storage. */
      }
    }
    void connect();
    timer = setInterval(() => {
      now = Date.now();
    }, 1000);
  });
  onDestroy(() => {
    destroyed = true;
    generation++;
    clearInterval(timer);
    stopSocket();
  });
</script>

<svelte:window on:popstate={() => (route = parseRoute(location.pathname))} />

<div class="app">
  <Header {feedStatus} {lastMessageAt} {now} {loading} {resuming} onTogglePause={togglePause} />
  <main>
    {#if requiresAuth}
      <section class="auth-panel panel" aria-labelledby="auth-heading">
        <div class="eyebrow">Protected dashboard</div>
        <h1 id="auth-heading">Connect to cmt-top</h1>
        <p class="muted">Use the access token configured by your dashboard operator.</p>
        <form on:submit|preventDefault={authenticate}>
          <label for="access-token">Access token</label>
          <div class="auth-fields">
            <input
              id="access-token"
              type="password"
              bind:value={enteredToken}
              autocomplete="current-password"
              required
            /><button type="submit" class="primary">Connect</button>
          </div>
        </form>
        <p class="small muted">The token is kept for this browser tab's session.</p>
        {#if error}<p class="error-text" role="alert">{error}</p>{/if}
      </section>
    {:else}
      {#if loading && !$dashboard.receivedAt}<div class="notice" role="status">
          Connecting to the dashboard and loading the latest snapshot…
        </div>{/if}
      {#if error}<div class="notice warning" role="alert">
          <span>{error}</span><button on:click={connect} disabled={loading}>Retry connection</button
          >
        </div>{/if}
      {#if $pausedAt}<div class="notice warning">
          <span
            ><strong>View paused</strong> at {new Date($pausedAt).toLocaleTimeString()}. Collection
            continues in the background.</span
          ><button on:click={togglePause} disabled={resuming}
            >{resuming ? "Refreshing…" : "Resume with fresh data"}</button
          >
        </div>
      {:else if $dashboard.receivedAt && (mode === "stale" || mode === "unavailable")}
        <div class="notice warning">
          <span
            ><strong
              >{mode === "stale"
                ? "Upstream data is stale."
                : "Upstream data is unavailable."}</strong
            >
            {mode === "stale"
              ? "Last-known values remain visible."
              : "Waiting for a successful RPC response."}</span
          ><a href="#node-details" on:click={openNodeDetails}>View connection details</a>
        </div>
      {:else if $dashboard.receivedAt && mode === "polling"}<div class="notice">
          <span
            ><strong>Polling fallback</strong> · HTTP responses are supplying fresh data while stream
            events are unavailable or delayed. Live votes may be incomplete.</span
          >
        </div>{/if}
      {#if $dashboard.chain.catchingUp}<div class="notice warning">
          The connected node is catching up. Its view may lag the chain.
        </div>{/if}
      {#if route.page === "rounds"}
        {#if !loading && $dashboard.receivedAt}
          {#key `${route.height}/${token}`}
            <BlockRounds
              bind:this={investigationPage}
              height={route.height}
              {token}
              {now}
              onNavigate={navigate}
              onAuthFailure={authFailed}
            />
          {/key}
        {/if}
      {:else}
        <div class="dashboard-investigate">
          <span class="small muted">Investigating a stalled block or upgrade?</span
          >{#if $dashboard.height > 0}<a
              class="button-link"
              href={roundsPath($dashboard.height)}
              on:click={(event) => navigateLink(event, roundsPath($dashboard.height))}
              >Investigate block {formatHeight($dashboard.height)} →</a
            >{:else}<a
              class="button-link"
              href={roundsPath(1)}
              on:click={(event) => navigateLink(event, roundsPath(1))}>Open block investigation →</a
            >{/if}
        </div>
        <ChainCard />
        <div class="workspace">
          <ValidatorTable />
          <aside><DivergencePanel {now} /><RPCPanel {now} /><BlockHistory /></aside>
        </div>
      {/if}
      <details class="panel node-details" id="node-details">
        <summary
          >Node &amp; connection details {#if Object.keys($dashboard.errors).length}<span
              class="badge warning-text"
              >{Object.keys($dashboard.errors).length} diagnostic{Object.keys($dashboard.errors)
                .length === 1
                ? ""
                : "s"}</span
            >{/if}</summary
        >
        <dl class="detail-grid">
          <dt>Chain</dt>
          <dd>{$dashboard.chain.network || "Not received"}</dd>
          <dt>CometBFT</dt>
          <dd>{$dashboard.chain.cometVersion || "Not received"}</dd>
          <dt>HTTP endpoint</dt>
          <dd class="mono break">
            {$dashboard.health.httpEndpoint || $dashboard.activeRPC || "Not received"}
          </dd>
          <dt>Stream endpoint</dt>
          <dd class="mono break">{$dashboard.health.wsEndpoint || "Not connected"}</dd>
          <dt>Connected node validator</dt>
          <dd class="mono break">{$dashboard.chain.ourValidator || "Not provided"}</dd>
          <dt>Last successful data</dt>
          <dd>
            {$dashboard.health.lastSuccessAt
              ? new Date($dashboard.health.lastSuccessAt).toLocaleString()
              : "None"}
          </dd>
          <dt>Last stream event</dt>
          <dd>
            {$dashboard.health.lastEventAt
              ? new Date($dashboard.health.lastEventAt).toLocaleString()
              : "None"}
          </dd>
          {#if $dashboard.health.lastError}<dt>Connection diagnostic</dt>
            <dd class="break">{$dashboard.health.lastError}</dd>{/if}
          {#each Object.entries($dashboard.errors) as [source, message]}<dt>{source}</dt>
            <dd class="break">{message}</dd>{/each}
        </dl>
      </details>
      <footer>
        <span
          >Observed from the configured RPC · absence from this feed is not proof of a missed block.</span
        ><span>cmt-top</span>
      </footer>
    {/if}
  </main>
</div>
