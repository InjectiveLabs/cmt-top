import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { get } from "svelte/store";
import { APIError, fetchSession } from "./api";
import { createWS } from "./ws";
import type { Envelope } from "./types";
vi.mock("./api", async (original) => ({
  ...(await original<typeof import("./api")>()),
  fetchSession: vi.fn(),
}));
class FakeWebSocket {
  static OPEN = 1;
  static instances: FakeWebSocket[] = [];
  readyState = 0;
  onopen: (() => void) | null = null;
  onmessage: ((event: { data: string }) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  send = vi.fn<(data: string) => void>();
  constructor(readonly url: string) {
    FakeWebSocket.instances.push(this);
  }
  open() {
    this.readyState = 1;
    this.onopen?.();
  }
  message(type: string, seq: number, payload: unknown = {}) {
    this.onmessage?.({ data: JSON.stringify({ type, seq, payload }) });
  }
  close() {
    this.readyState = 3;
    this.onclose?.();
  }
  commands() {
    return this.send.mock.calls.map(([value]) => JSON.parse(value));
  }
}
const capable = {
  schemaVersion: 1,
  serverEpoch: "epoch-a",
  capabilities: ["context-v1", "rounds-compact-v1", "rounds-capture-v1"],
};
const flush = () => vi.advanceTimersByTimeAsync(0);
beforeEach(() => {
  vi.useFakeTimers();
  vi.spyOn(Math, "random").mockReturnValue(0);
  vi.stubGlobal("location", { origin: "http://localhost:8080", protocol: "http:" });
  vi.stubGlobal("WebSocket", FakeWebSocket);
  FakeWebSocket.instances = [];
  vi.mocked(fetchSession).mockReset().mockResolvedValue(capable);
});
afterEach(() => {
  vi.clearAllTimers();
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});
describe("websocket lifecycle", () => {
  it("uses a small probe and waits for a baseline, coalescing sequence repairs", async () => {
    const client = createWS("/ws"),
      messages: Envelope[] = [];
    client.onMessage((message) => messages.push(message));
    await flush();
    const socket = FakeWebSocket.instances[0];
    socket.open();
    socket.message("vote.received", 1);
    expect(messages).toHaveLength(0);
    socket.message("state.snapshot", 2, capable);
    socket.message("vote.received", 3);
    socket.message("vote.received", 5);
    socket.message("vote.received", 6);
    socket.onmessage?.({ data: "{bad" });
    socket.onmessage?.({ data: "{bad" });
    expect(socket.commands().filter((c) => c.type === "resync")).toHaveLength(1);
    expect(messages.map((e) => e.seq)).toEqual([2, 3]);
    socket.message("state.snapshot", 7, capable);
    socket.message("vote.received", 8);
    socket.message("vote.received", 8);
    expect(messages.map((e) => e.seq)).toEqual([2, 3, 7, 8]);
    expect(get(client.status)).toBe("open");
    expect(fetchSession).toHaveBeenCalledOnce();
    client.close();
  });
  it("renegotiates an already-open investigation tab after rollback", async () => {
    const client = createWS("/ws");
    client.setProfile("context");
    await flush();
    const first = FakeWebSocket.instances[0];
    first.open();
    first.message("state.snapshot", 1, capable);
    expect(first.commands()).toEqual([
      { type: "subscribe", channels: ["context"] },
      { type: "unsubscribe", channels: ["state", "blocks", "divergence", "votes"] },
      { type: "ping" },
    ]);
    expect(get(client.session).capabilities).toContain("rounds-compact-v1");
    first.close();
    await vi.advanceTimersByTimeAsync(250);
    const second = FakeWebSocket.instances[1];
    second.open();
    second.message("state.snapshot", 1, {});
    expect(get(client.session).legacy).toBe(true);
    expect(second.commands()).toEqual([]);
    expect(get(client.status)).toBe("open");
    client.close();
  });
  it("waits for the ordered context barrier and repairs a mismatched epoch", async () => {
    const client = createWS("/ws"),
      messages: Envelope[] = [];
    client.setProfile("context");
    client.onMessage((message) => messages.push(message));
    await flush();
    const socket = FakeWebSocket.instances[0];
    socket.open();
    socket.message("state.snapshot", 1, capable);
    socket.message("context.snapshot", 2, { serverEpoch: "epoch-a" });
    expect(messages.map((message) => message.type)).toEqual(["state.snapshot"]);
    socket.message("pong", 3);
    socket.message("context.snapshot", 4, { serverEpoch: "epoch-a" });
    expect(messages.map((message) => message.type)).toEqual(["state.snapshot", "context.snapshot"]);
    socket.message("context.snapshot", 5, { serverEpoch: "epoch-b" });
    expect(get(client.status)).toBe("resyncing");
    expect(socket.commands().filter((command) => command.type === "resync")).toHaveLength(1);
    client.close();
  });
  it("honors Retry-After during bootstrap and stops on 401", async () => {
    vi.mocked(fetchSession).mockRejectedValueOnce(new APIError(503, 2000));
    const client = createWS("/ws");
    await flush();
    expect(FakeWebSocket.instances).toHaveLength(0);
    await vi.advanceTimersByTimeAsync(1999);
    expect(fetchSession).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(FakeWebSocket.instances).toHaveLength(1);
    client.close();
    const onAuth = vi.fn();
    vi.mocked(fetchSession).mockRejectedValue(new APIError(401));
    const rejected = createWS("/ws", "bad", onAuth);
    await vi.advanceTimersByTimeAsync(20000);
    expect(onAuth).toHaveBeenCalledOnce();
    expect(get(rejected.status)).toBe("closed");
    rejected.close();
  });
  it("does not reset backoff when accepted sockets keep flapping", async () => {
    const client = createWS("/ws");
    await flush();
    let socket = FakeWebSocket.instances[0];
    socket.open();
    socket.message("state.snapshot", 1);
    socket.close();
    await vi.advanceTimersByTimeAsync(250);
    socket = FakeWebSocket.instances[1];
    socket.open();
    socket.message("state.snapshot", 1);
    socket.close();
    await vi.advanceTimersByTimeAsync(499);
    expect(FakeWebSocket.instances).toHaveLength(2);
    await vi.advanceTimersByTimeAsync(1);
    expect(FakeWebSocket.instances).toHaveLength(3);
    client.close();
  });
  it("resets backoff after a stable authoritative connection", async () => {
    const client = createWS("/ws");
    await flush();
    FakeWebSocket.instances[0].close();
    await vi.advanceTimersByTimeAsync(250);
    const socket = FakeWebSocket.instances[1];
    socket.open();
    socket.message("state.snapshot", 1);
    await vi.advanceTimersByTimeAsync(10000);
    socket.close();
    await vi.advanceTimersByTimeAsync(250);
    expect(FakeWebSocket.instances).toHaveLength(3);
    client.close();
  });
  it("times out missing baselines and retries", async () => {
    const client = createWS("/ws");
    await flush();
    FakeWebSocket.instances[0].open();
    await vi.advanceTimersByTimeAsync(8250);
    expect(FakeWebSocket.instances).toHaveLength(2);
    client.close();
  });
  it("bounds a WebSocket handshake that never opens", async () => {
    const client = createWS("/ws");
    await flush();
    await vi.advanceTimersByTimeAsync(8250);
    expect(FakeWebSocket.instances[0].readyState).toBe(3);
    expect(FakeWebSocket.instances).toHaveLength(2);
    client.close();
  });
  it("suspends hidden traffic and restores one authoritative baseline", async () => {
    const client = createWS("/ws");
    await flush();
    const socket = FakeWebSocket.instances[0];
    socket.open();
    socket.message("state.snapshot", 1, capable);
    client.setProfile("hidden");
    expect(get(client.status)).toBe("suspended");
    client.setProfile("dashboard");
    expect(socket.commands().filter((c) => c.type === "resync")).toHaveLength(1);
    socket.message("state.snapshot", 3, capable);
    expect(socket.commands().filter((c) => c.type === "resync")).toHaveLength(1);
    client.setProfile("hidden");
    await vi.advanceTimersByTimeAsync(30000);
    expect(socket.readyState).toBe(3);
    expect(FakeWebSocket.instances).toHaveLength(1);
    client.setProfile("context");
    await flush();
    expect(FakeWebSocket.instances).toHaveLength(2);
    client.close();
  });
  it("coalesces explicit refreshes and resolves only on authoritative snapshots", async () => {
    const client = createWS("/ws");
    await flush();
    const socket = FakeWebSocket.instances[0];
    socket.open();
    socket.message("state.snapshot", 1, capable);
    const first = client.refresh(),
      second = client.refresh();
    expect(socket.commands().filter((c) => c.type === "resync")).toHaveLength(1);
    socket.message("state.snapshot", 4, capable);
    await Promise.all([first, second]);
    client.close();
  });
  it("ignores probes finishing after disposal", async () => {
    let reject!: (e: Error) => void;
    vi.mocked(fetchSession).mockReturnValue(
      new Promise((_, r) => {
        reject = r;
      }),
    );
    const onAuth = vi.fn(),
      client = createWS("/ws", "old", onAuth);
    client.close();
    reject(new APIError(401));
    await flush();
    expect(onAuth).not.toHaveBeenCalled();
    expect(FakeWebSocket.instances).toHaveLength(0);
  });
  it("fails unsupported marked protocols explicitly", async () => {
    const client = createWS("/ws");
    await flush();
    const socket = FakeWebSocket.instances[0];
    socket.open();
    socket.message("state.snapshot", 1, { ...capable, schemaVersion: 2 });
    expect(get(client.status)).toBe("closed");
    expect(get(client.error)).toContain("unsupported");
    await vi.advanceTimersByTimeAsync(20000);
    expect(FakeWebSocket.instances).toHaveLength(1);
    client.close();
  });
});
