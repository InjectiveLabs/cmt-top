import { writable, type Writable } from "svelte/store";

export type ConnectionStatus = "connecting" | "open" | "reconnecting" | "closed";

export interface Envelope {
  type: string;
  ts: string;
  height?: number;
  round?: number;
  seq: number;
  payload: any;
}

export interface WSClient {
  status: Writable<ConnectionStatus>;
  lastSeq: Writable<number>;
  onMessage: (fn: (e: Envelope) => void) => () => void;
  send: (msg: any) => void;
  close: () => void;
}

export function createWS(url: string, token?: string): WSClient {
  const status = writable<ConnectionStatus>("connecting");
  const lastSeq = writable(0);
  let lastSeqVal = 0;
  lastSeq.subscribe((v) => (lastSeqVal = v));
  const handlers = new Set<(e: Envelope) => void>();

  let ws: WebSocket | null = null;
  let attempt = 0;
  let closed = false;
  let reconnectTimer: number | undefined;

  const open = () => {
    if (closed) return;
    const u = new URL(url, location.origin);
    if (token) u.searchParams.set("token", token);
    if (lastSeqVal > 0) u.searchParams.set("since", String(lastSeqVal));
    u.protocol = u.protocol.replace("http", "ws");
    status.set(attempt === 0 ? "connecting" : "reconnecting");
    ws = new WebSocket(u.toString());
    ws.onopen = () => {
      attempt = 0;
      status.set("open");
    };
    ws.onmessage = (ev) => {
      try {
        const env = JSON.parse(ev.data) as Envelope;
        if (env.seq > lastSeqVal) lastSeq.set(env.seq);
        handlers.forEach((h) => h(env));
      } catch {
        // ignore
      }
    };
    ws.onclose = () => {
      if (closed) return;
      status.set("reconnecting");
      const backoff = Math.min(8000, 250 * 2 ** Math.min(attempt, 5));
      const jitter = Math.random() * (backoff / 4);
      reconnectTimer = window.setTimeout(open, backoff + jitter);
      attempt++;
    };
    ws.onerror = () => {
      ws?.close();
    };
  };

  open();

  return {
    status,
    lastSeq,
    onMessage(fn) {
      handlers.add(fn);
      return () => handlers.delete(fn);
    },
    send(msg) {
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify(msg));
      }
    },
    close() {
      closed = true;
      if (reconnectTimer) clearTimeout(reconnectTimer);
      ws?.close();
      status.set("closed");
    },
  };
}
