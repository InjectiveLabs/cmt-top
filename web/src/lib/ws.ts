import { writable, type Writable } from "svelte/store";
import { APIError, fetchSnapshot } from "./api";
import type { Envelope } from "./types";
export type { Envelope } from "./types";
export type ConnectionStatus = "connecting" | "open" | "reconnecting" | "resyncing" | "closed";
export interface WSClient {
  status: Writable<ConnectionStatus>;
  lastMessageAt: Writable<number>;
  onMessage: (fn: (e: Envelope) => void) => () => void;
  send: (message: unknown) => void;
  close: () => void;
}
export function createWS(url: string, token?: string, onAuthFailure?: () => void): WSClient {
  const status = writable<ConnectionStatus>("connecting"),
    lastMessageAt = writable(0);
  const handlers = new Set<(e: Envelope) => void>();
  let ws: WebSocket | null = null,
    attempt = 0,
    closed = false,
    sequence: number | null = null,
    resyncing = true;
  let reconnectTimer: ReturnType<typeof setTimeout> | undefined;
  const send = (message: unknown) => {
    if (ws?.readyState === WebSocket.OPEN) ws.send(JSON.stringify(message));
  };
  const open = () => {
    if (closed) return;
    const u = new URL(url, location.origin);
    u.protocol = location.protocol === "https:" ? "wss:" : "ws:";
    if (token) u.searchParams.set("token", token);
    sequence = null;
    resyncing = true;
    status.set(attempt === 0 ? "connecting" : "reconnecting");
    const socket = new WebSocket(u.toString());
    ws = socket;
    let connected = false;
    socket.onopen = () => {
      if (closed || ws !== socket) return;
      connected = true;
      attempt = 0;
      status.set("resyncing");
    };
    socket.onmessage = (event) => {
      if (closed || ws !== socket) return;
      let env: Envelope;
      try {
        env = JSON.parse(event.data) as Envelope;
      } catch {
        send({ type: "resync" });
        resyncing = true;
        status.set("resyncing");
        return;
      }
      if (!env || typeof env.type !== "string" || typeof env.seq !== "number") return;
      lastMessageAt.set(Date.now());
      if (env.type === "state.snapshot") {
        sequence = env.seq;
        resyncing = false;
        status.set("open");
      } else {
        if (resyncing) return;
        if (sequence !== null && env.seq <= sequence) return;
        if (sequence !== null && env.seq !== sequence + 1) {
          resyncing = true;
          status.set("resyncing");
          send({ type: "resync" });
          return;
        }
        sequence = env.seq;
      }
      handlers.forEach((handler) => handler(env));
    };
    socket.onclose = async () => {
      if (closed || ws !== socket) return;
      status.set("reconnecting");
      // Browsers hide WS handshake status. Check a failed handshake through
      // REST so invalid credentials do not reconnect indefinitely.
      if (!connected) {
        try {
          await fetchSnapshot(token, AbortSignal.timeout(5000));
        } catch (error) {
          if (closed || ws !== socket) return;
          if (error instanceof APIError && error.status === 401) {
            closed = true;
            status.set("closed");
            onAuthFailure?.();
            return;
          }
        }
      }
      if (closed) return;
      const backoff = Math.min(8000, 250 * 2 ** Math.min(attempt++, 5));
      reconnectTimer = setTimeout(open, backoff + (Math.random() * backoff) / 4);
    };
    socket.onerror = () => socket.close();
  };
  open();
  return {
    status,
    lastMessageAt,
    onMessage(fn) {
      handlers.add(fn);
      return () => {
        handlers.delete(fn);
      };
    },
    send,
    close() {
      closed = true;
      if (reconnectTimer) clearTimeout(reconnectTimer);
      ws?.close();
      handlers.clear();
      status.set("closed");
    },
  };
}
