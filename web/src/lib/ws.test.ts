import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { get } from "svelte/store";
import { APIError, fetchSnapshot } from "./api";
import { createWS } from "./ws";
import type { Envelope } from "./types";

vi.mock("./api", async (original) => {
  const actual = await original<typeof import("./api")>();
  return { ...actual, fetchSnapshot: vi.fn() };
});

class FakeWebSocket {
  static OPEN = 1;
  static instances: FakeWebSocket[] = [];
  readyState = 0;
  onopen: (() => void) | null = null;
  onmessage: ((event: { data: string }) => void) | null = null;
  onclose: (() => void | Promise<void>) | null = null;
  onerror: (() => void) | null = null;
  send = vi.fn<(data: string) => void>();
  constructor(readonly url: string) {
    FakeWebSocket.instances.push(this);
  }
  open() {
    this.readyState = FakeWebSocket.OPEN;
    this.onopen?.();
  }
  message(type: string, seq: number) {
    this.onmessage?.({ data: JSON.stringify({ type, seq, payload: {} }) });
  }
  close() {
    this.readyState = 3;
    void this.onclose?.();
  }
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.spyOn(Math, "random").mockReturnValue(0);
  vi.stubGlobal("location", { origin: "http://localhost:8080", protocol: "http:" });
  vi.stubGlobal("WebSocket", FakeWebSocket);
  FakeWebSocket.instances = [];
  vi.mocked(fetchSnapshot).mockReset();
});

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("websocket snapshot reconciliation", () => {
  it("waits for a baseline and drops patches after a sequence gap until resync", () => {
    const client = createWS("/ws"),
      messages: Envelope[] = [];
    client.onMessage((message) => messages.push(message));
    const socket = FakeWebSocket.instances[0];
    socket.open();
    expect(get(client.status)).toBe("resyncing");
    socket.message("vote.received", 1);
    expect(messages).toHaveLength(0);
    socket.message("state.snapshot", 2);
    socket.message("vote.received", 3);
    expect(get(client.status)).toBe("open");
    socket.message("vote.received", 5);
    expect(get(client.status)).toBe("resyncing");
    expect(socket.send).toHaveBeenLastCalledWith(JSON.stringify({ type: "resync" }));
    socket.message("round.changed", 6);
    expect(messages.map((message) => message.seq)).toEqual([2, 3]);
    socket.message("state.snapshot", 7);
    socket.message("vote.received", 8);
    expect(messages.map((message) => message.seq)).toEqual([2, 3, 7, 8]);
    expect(get(client.status)).toBe("open");
    client.close();
  });

  it("ignores duplicate patches and resets the sequence when the server restarts", async () => {
    const client = createWS("/ws", "session-token"),
      messages: Envelope[] = [];
    client.onMessage((message) => messages.push(message));
    const first = FakeWebSocket.instances[0];
    first.open();
    first.message("state.snapshot", 100);
    first.message("vote.received", 101);
    first.message("vote.received", 101);
    expect(messages.map((message) => message.seq)).toEqual([100, 101]);
    first.close();
    await vi.advanceTimersByTimeAsync(250);
    const second = FakeWebSocket.instances[1];
    expect(second).toBeDefined();
    const url = new URL(second.url);
    expect(url.searchParams.get("token")).toBe("session-token");
    expect(url.searchParams.has("since")).toBe(false);
    second.open();
    second.message("state.snapshot", 1);
    second.message("vote.received", 2);
    expect(messages.map((message) => message.seq)).toEqual([100, 101, 1, 2]);
    expect(get(client.status)).toBe("open");
    client.close();
  });

  it("requests a new baseline after malformed JSON instead of applying later patches", () => {
    const client = createWS("/ws"),
      messages: Envelope[] = [];
    client.onMessage((message) => messages.push(message));
    const socket = FakeWebSocket.instances[0];
    socket.open();
    socket.message("state.snapshot", 1);
    socket.onmessage?.({ data: "{broken" });
    socket.message("vote.received", 2);
    expect(messages).toHaveLength(1);
    expect(get(client.status)).toBe("resyncing");
    expect(socket.send).toHaveBeenCalledWith(JSON.stringify({ type: "resync" }));
    socket.message("state.snapshot", 3);
    expect(get(client.status)).toBe("open");
    client.close();
  });

  it("recognizes an unauthorized handshake and stops reconnecting", async () => {
    vi.mocked(fetchSnapshot).mockRejectedValue(new APIError(401));
    const onAuthFailure = vi.fn(),
      client = createWS("/ws", "expired", onAuthFailure);
    FakeWebSocket.instances[0].close();
    await vi.advanceTimersByTimeAsync(10000);
    expect(onAuthFailure).toHaveBeenCalledOnce();
    expect(get(client.status)).toBe("closed");
    expect(FakeWebSocket.instances).toHaveLength(1);
    client.close();
  });

  it("cancels pending reconnects when the view is disposed", async () => {
    const client = createWS("/ws"),
      socket = FakeWebSocket.instances[0];
    socket.open();
    socket.message("state.snapshot", 1);
    socket.close();
    client.close();
    await vi.advanceTimersByTimeAsync(10000);
    expect(FakeWebSocket.instances).toHaveLength(1);
    expect(get(client.status)).toBe("closed");
  });

  it("ignores an authentication probe that finishes after the client is disposed", async () => {
    let rejectProbe: (error: unknown) => void = () => {};
    vi.mocked(fetchSnapshot).mockReturnValue(
      new Promise((_, reject) => {
        rejectProbe = reject;
      }),
    );
    const onAuthFailure = vi.fn();
    const client = createWS("/ws", "old-token", onAuthFailure);
    FakeWebSocket.instances[0].close();
    client.close();
    rejectProbe(new APIError(401));
    await vi.advanceTimersByTimeAsync(0);
    expect(onAuthFailure).not.toHaveBeenCalled();
    expect(get(client.status)).toBe("closed");
    expect(FakeWebSocket.instances).toHaveLength(1);
  });
});
