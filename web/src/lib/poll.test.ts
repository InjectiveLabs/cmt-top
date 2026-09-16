import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createPoller } from "./poll";
import { APIError } from "./api";
beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());
const deferred = <T>() => {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
};

describe("investigation polling lifecycle", () => {
  it("never overlaps requests and waits a full interval after completion", async () => {
    const first = deferred<number>(),
      request = vi.fn().mockReturnValueOnce(first.promise).mockResolvedValue(2),
      onData = vi.fn();
    const poller = createPoller({
      request,
      onData,
      onError: vi.fn(),
      onUnauthorized: vi.fn(),
      timeoutMs: 30000,
    });
    await vi.advanceTimersByTimeAsync(5000);
    expect(request).toHaveBeenCalledTimes(1);
    expect(await poller.refresh()).toBe(false);
    first.resolve(1);
    await vi.advanceTimersByTimeAsync(999);
    expect(request).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(request).toHaveBeenCalledTimes(2);
    expect(onData.mock.calls.map(([value]) => value)).toEqual([1, 2]);
    poller.dispose();
  });
  it("does not multiply polling timers after manual refreshes", async () => {
    const request = vi.fn().mockResolvedValue(1);
    const poller = createPoller({
      request,
      onData: vi.fn(),
      onError: vi.fn(),
      onUnauthorized: vi.fn(),
    });
    await vi.advanceTimersByTimeAsync(500);
    await poller.refresh();
    await vi.advanceTimersByTimeAsync(500);
    expect(request).toHaveBeenCalledTimes(2);
    await poller.refresh();
    await vi.advanceTimersByTimeAsync(999);
    expect(request).toHaveBeenCalledTimes(3);
    await vi.advanceTimersByTimeAsync(1);
    expect(request).toHaveBeenCalledTimes(4);
    await vi.advanceTimersByTimeAsync(1000);
    expect(request).toHaveBeenCalledTimes(5);
    poller.dispose();
  });
  it("freezes on pause and refreshes explicitly before resuming", async () => {
    const first = deferred<number>(),
      request = vi.fn().mockReturnValueOnce(first.promise).mockResolvedValue(2),
      onData = vi.fn();
    const poller = createPoller({ request, onData, onError: vi.fn(), onUnauthorized: vi.fn() });
    poller.setPaused(true);
    expect(request.mock.calls[0][0].aborted).toBe(true);
    first.resolve(1);
    await vi.advanceTimersByTimeAsync(5000);
    expect(onData).not.toHaveBeenCalled();
    expect(request).toHaveBeenCalledTimes(1);
    expect(await poller.refresh(true)).toBe(true);
    expect(onData).toHaveBeenCalledWith(2);
    await vi.advanceTimersByTimeAsync(5000);
    expect(request).toHaveBeenCalledTimes(2);
    poller.setPaused(false);
    await vi.advanceTimersByTimeAsync(0);
    expect(request).toHaveBeenCalledTimes(3);
    poller.dispose();
  });
  it("discards late data and auth rejections after the page is disposed", async () => {
    for (const unauthorized of [false, true]) {
      const attempt = deferred<number>(),
        onData = vi.fn(),
        onUnauthorized = vi.fn();
      const poller = createPoller({
        request: () => attempt.promise,
        onData,
        onError: vi.fn(),
        onUnauthorized,
      });
      poller.dispose();
      if (unauthorized) attempt.reject(new APIError(401));
      else attempt.resolve(1);
      await vi.advanceTimersByTimeAsync(10000);
      expect(onData).not.toHaveBeenCalled();
      expect(onUnauthorized).not.toHaveBeenCalled();
    }
  });
  it("stops on unauthorized instead of retrying indefinitely", async () => {
    const request = vi.fn().mockRejectedValue(new APIError(401)),
      onUnauthorized = vi.fn();
    const poller = createPoller({ request, onData: vi.fn(), onError: vi.fn(), onUnauthorized });
    await vi.advanceTimersByTimeAsync(10000);
    expect(request).toHaveBeenCalledTimes(1);
    expect(onUnauthorized).toHaveBeenCalledOnce();
    expect(await poller.refresh()).toBe(false);
    poller.dispose();
  });
  it("times out an unresponsive fetch and continues bounded retries", async () => {
    const onError = vi.fn(),
      request = vi.fn(
        (signal: AbortSignal) =>
          new Promise((_, reject) =>
            signal.addEventListener("abort", () =>
              reject(new DOMException("Aborted", "AbortError")),
            ),
          ),
      );
    const poller = createPoller({ request, onData: vi.fn(), onError, onUnauthorized: vi.fn() });
    await vi.advanceTimersByTimeAsync(8000);
    expect(onError).toHaveBeenCalledOnce();
    expect(request).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1000);
    expect(request).toHaveBeenCalledTimes(2);
    poller.dispose();
  });
});
