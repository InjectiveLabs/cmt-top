import { writable, type Writable } from "svelte/store";
import { APIError, fetchSession } from "./api";
import { legacySession, sessionFromBaseline, type SessionInfo } from "./session";
import type { Envelope } from "./types";
export type { Envelope } from "./types";
export type ConnectionStatus =
  | "connecting"
  | "open"
  | "reconnecting"
  | "resyncing"
  | "suspended"
  | "closed";
export type StreamProfile = "dashboard" | "context" | "hidden";
export interface WSClient {
  status: Writable<ConnectionStatus>;
  lastMessageAt: Writable<number>;
  session: Writable<SessionInfo>;
  error: Writable<string>;
  onMessage: (fn: (e: Envelope) => void) => () => void;
  send: (message: unknown) => void;
  setProfile: (profile: StreamProfile) => void;
  refresh: () => Promise<void>;
  close: () => void;
}
const dashboardChannels = ["state", "blocks", "divergence", "votes"];
export function createWS(url: string, token?: string, onAuthFailure?: () => void): WSClient {
  const status = writable<ConnectionStatus>("connecting"),
    lastMessageAt = writable(0);
  const session = writable<SessionInfo>(legacySession()),
    error = writable("");
  const handlers = new Set<(e: Envelope) => void>();
  const waiters = new Set<{ resolve: () => void; reject: (reason: Error) => void }>();
  let info = legacySession(),
    ws: WebSocket | null = null,
    attempt = 0,
    closed = false,
    opening = false;
  let sequence: number | null = null,
    resyncing = true,
    requestedRepair = false,
    profileBarrier = false;
  let desired: StreamProfile = "dashboard",
    applied: StreamProfile = "dashboard";
  let reconnectTimer: ReturnType<typeof setTimeout> | undefined;
  let baselineTimer: ReturnType<typeof setTimeout> | undefined;
  let stableTimer: ReturnType<typeof setTimeout> | undefined;
  let hiddenTimer: ReturnType<typeof setTimeout> | undefined;
  let probe: AbortController | undefined;
  const send = (message: unknown) => {
    if (ws?.readyState === WebSocket.OPEN) ws.send(JSON.stringify(message));
  };
  function clearConnectionTimers() {
    clearTimeout(baselineTimer);
    clearTimeout(stableTimer);
  }
  function armBaselineTimeout() {
    clearTimeout(baselineTimer);
    const socket = ws;
    baselineTimer = setTimeout(() => {
      if (ws === socket) socket?.close();
    }, 8000);
  }
  function requestRepair() {
    if (requestedRepair || ws?.readyState !== WebSocket.OPEN) return;
    requestedRepair = true;
    resyncing = true;
    status.set("resyncing");
    send({ type: "resync" });
    armBaselineTimeout();
  }
  function applyProfile() {
    if (!ws || ws.readyState !== WebSocket.OPEN || resyncing) return;
    const target =
      desired === "context" && !info.capabilities.includes("context-v1") ? "dashboard" : desired;
    if (target === applied) return;
    if (target === "context") {
      send({ type: "subscribe", channels: ["context"] });
      send({ type: "unsubscribe", channels: dashboardChannels });
      profileBarrier = true;
      send({ type: "ping" }); // Ordered pong is the server subscription barrier.
    } else if (target === "hidden") {
      send({ type: "unsubscribe", channels: [...dashboardChannels, "context"] });
      send({ type: "ping" });
    } else {
      send({ type: "subscribe", channels: dashboardChannels });
      send({ type: "unsubscribe", channels: ["context"] });
      requestRepair();
    }
    applied = target;
  }
  function reconnect(retryAfterMs = 0) {
    if (closed || desired === "hidden") {
      if (!closed) status.set("suspended");
      return;
    }
    status.set("reconnecting");
    const delay = Math.max(retryAfterMs, Math.min(8000, 250 * 2 ** Math.min(attempt++, 5)));
    clearTimeout(reconnectTimer);
    reconnectTimer = setTimeout(() => void open(), delay * (1 + Math.random() / 4));
  }
  async function open() {
    if (closed || opening || desired === "hidden") return;
    opening = true;
    status.set(attempt === 0 ? "connecting" : "reconnecting");
    const controller = new AbortController();
    probe = controller;
    const timeout = setTimeout(() => controller.abort(), 5000);
    try {
      await fetchSession(token, controller.signal);
      if (closed || (desired as StreamProfile) === "hidden" || controller.signal.aborted) return;
      const u = new URL(url, location.origin);
      u.protocol = location.protocol === "https:" ? "wss:" : "ws:";
      if (token) u.searchParams.set("token", token);
      sequence = null;
      resyncing = true;
      requestedRepair = false;
      applied = "dashboard";
      profileBarrier = false;
      const socket = new WebSocket(u.toString());
      ws = socket;
      armBaselineTimeout();
      socket.onopen = () => {
        if (closed || ws !== socket) return;
        status.set("resyncing");
        armBaselineTimeout();
      };
      socket.onmessage = (event) => {
        if (closed || ws !== socket) return;
        let env: Envelope;
        try {
          env = JSON.parse(event.data) as Envelope;
        } catch {
          requestRepair();
          return;
        }
        if (!env || typeof env.type !== "string" || !Number.isSafeInteger(env.seq) || env.seq < 1) {
          requestRepair();
          return;
        }
        lastMessageAt.set(Date.now());
        if (env.type === "state.snapshot") {
          try {
            info = sessionFromBaseline(env.payload);
          } catch (failure) {
            error.set((failure as Error).message);
            closed = true;
            clearConnectionTimers();
            socket.close();
            status.set("closed");
            waiters.forEach((waiter) => waiter.reject(failure as Error));
            waiters.clear();
            return;
          }
          sequence = env.seq;
          requestedRepair = false;
          profileBarrier = false;
          resyncing = false;
          clearTimeout(baselineTimer);
          // Re-negotiate every authoritative baseline, including live-tab rollback.
          session.set(info);
          status.set(desired === "hidden" ? "suspended" : "open");
          error.set("");
          if (!stableTimer)
            stableTimer = setTimeout(() => {
              attempt = 0;
              stableTimer = undefined;
            }, 10000);
          handlers.forEach((handler) => handler(env));
          waiters.forEach((waiter) => waiter.resolve());
          waiters.clear();
          applyProfile();
          return;
        }
        if (resyncing) return;
        if (sequence !== null && env.seq <= sequence) return;
        if (sequence !== null && env.seq !== sequence + 1) {
          requestRepair();
          return;
        }
        if (
          env.type === "context.snapshot" &&
          (env.payload as { serverEpoch?: string })?.serverEpoch !== info.serverEpoch
        ) {
          requestRepair();
          return;
        }
        sequence = env.seq;
        if (env.type === "pong") {
          profileBarrier = false;
          return;
        }
        if (desired !== "hidden" && !profileBarrier) handlers.forEach((handler) => handler(env));
      };
      socket.onclose = () => {
        if (closed || ws !== socket) return;
        ws = null;
        clearConnectionTimers();
        stableTimer = undefined;
        reconnect();
      };
      socket.onerror = () => socket.close();
    } catch (failure) {
      if (closed || (controller.signal.aborted && (desired as StreamProfile) === "hidden")) return;
      if (failure instanceof APIError && failure.status === 401) {
        closed = true;
        status.set("closed");
        waiters.forEach((waiter) => waiter.reject(failure));
        waiters.clear();
        onAuthFailure?.();
      } else {
        error.set(
          failure instanceof Error ? failure.message : "Could not connect. Retrying shortly.",
        );
        reconnect(failure instanceof APIError ? failure.retryAfterMs : 0);
      }
    } finally {
      clearTimeout(timeout);
      opening = false;
      if (probe === controller) probe = undefined;
    }
  }
  function setProfile(profile: StreamProfile) {
    if (closed || profile === desired) return;
    const wasHidden = desired === "hidden";
    desired = profile;
    clearTimeout(hiddenTimer);
    if (profile === "hidden") {
      clearTimeout(reconnectTimer);
      probe?.abort();
      applyProfile();
      status.set("suspended");
      hiddenTimer = setTimeout(() => {
        const socket = ws;
        ws = null;
        clearConnectionTimers();
        stableTimer = undefined;
        socket?.close();
      }, 30000);
    } else if (!ws) void open();
    else {
      if (wasHidden) {
        // Restore channels first; coalesce dashboard restoration and visibility repair.
        applyProfile();
        requestRepair();
      } else applyProfile();
    }
  }
  function refresh(): Promise<void> {
    if (closed || desired === "hidden")
      return Promise.reject(new Error("The browser feed is suspended or unavailable."));
    return new Promise((resolve, reject) => {
      const waiter = {
        resolve: () => {
          clearTimeout(timeout);
          resolve();
        },
        reject: (reason: Error) => {
          clearTimeout(timeout);
          reject(reason);
        },
      };
      const timeout = setTimeout(() => {
        waiters.delete(waiter);
        reject(new Error("Timed out refreshing the browser feed."));
      }, 12000);
      waiters.add(waiter);
      if (ws?.readyState === WebSocket.OPEN) requestRepair();
      else if (!opening && !reconnectTimer) void open();
    });
  }
  void open();
  return {
    status,
    lastMessageAt,
    session,
    error,
    send,
    setProfile,
    refresh,
    onMessage(fn) {
      handlers.add(fn);
      return () => {
        handlers.delete(fn);
      };
    },
    close() {
      closed = true;
      probe?.abort();
      clearTimeout(reconnectTimer);
      clearTimeout(hiddenTimer);
      clearConnectionTimers();
      ws?.close();
      waiters.forEach((waiter) => waiter.reject(new Error("Connection closed.")));
      waiters.clear();
      handlers.clear();
      status.set("closed");
    },
  };
}
