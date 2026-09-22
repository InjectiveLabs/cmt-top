import { APIError } from "./api";
export interface PollOptions<T> {
  request: (signal: AbortSignal) => Promise<T>;
  onData: (data: T) => void;
  onError: (error: unknown) => void;
  onUnauthorized: () => void;
  onLoading?: (loading: boolean) => void;
  paused?: boolean;
  hidden?: boolean;
  intervalMs?: number | (() => number);
  jitter?: number;
  timeoutMs?: number;
}
export function createPoller<T>(options: PollOptions<T>) {
  let paused = options.paused ?? false,
    hidden = options.hidden ?? false,
    disposed = false;
  let generation = 0,
    failures = 0,
    retryDelay = 0,
    refreshWhenIdle = false;
  let active: AbortController | undefined;
  let timer: ReturnType<typeof setTimeout> | undefined;
  const interval = () =>
    typeof options.intervalMs === "function" ? options.intervalMs() : (options.intervalMs ?? 1000);
  function schedule() {
    clearTimeout(timer);
    if (disposed || paused || hidden || active) return;
    const delay = Math.max(
      interval(),
      retryDelay,
      failures ? Math.min(30000, 1000 * 2 ** Math.min(failures - 1, 5)) : 0,
    );
    timer = setTimeout(() => void refresh(), delay * (1 + Math.random() * (options.jitter ?? 0)));
  }
  async function refresh(force = false): Promise<boolean> {
    if (disposed || hidden || (paused && !force) || active) return false;
    refreshWhenIdle = false;
    clearTimeout(timer);
    const run = ++generation,
      controller = new AbortController();
    active = controller;
    options.onLoading?.(true);
    const timeout = setTimeout(() => controller.abort(), options.timeoutMs ?? 8000);
    try {
      const data = await options.request(controller.signal);
      if (disposed || run !== generation || controller.signal.aborted) return false;
      failures = retryDelay = 0;
      options.onData(data);
      return true;
    } catch (error) {
      if (disposed || run !== generation) return false;
      if (error instanceof APIError && error.status === 401) {
        disposed = true;
        options.onUnauthorized();
      } else {
        failures++;
        retryDelay = error instanceof APIError ? error.retryAfterMs : 0;
        options.onError(error);
      }
      return false;
    } finally {
      clearTimeout(timeout);
      if (active === controller) active = undefined;
      if (!disposed) options.onLoading?.(false);
      if (refreshWhenIdle && !disposed && !paused && !hidden) {
        refreshWhenIdle = false;
        void refresh();
      } else schedule();
    }
  }
  function cancel() {
    generation++;
    clearTimeout(timer);
    active?.abort();
    options.onLoading?.(false);
  }
  function invalidate() {
    if (disposed || paused || hidden) return;
    if (active) {
      refreshWhenIdle = true;
      cancel();
    } else void refresh();
  }
  function setPaused(value: boolean, refreshOnResume = true) {
    if (paused === value || disposed) return;
    paused = value;
    if (value) cancel();
    else if (!hidden) {
      if (refreshOnResume) invalidate();
      else schedule();
    }
  }
  function setHidden(value: boolean) {
    if (hidden === value || disposed) return;
    hidden = value;
    if (value) cancel();
    else if (!paused) invalidate();
  }
  function dispose() {
    disposed = true;
    cancel();
  }
  void refresh();
  return { refresh, setPaused, setHidden, cancel, invalidate, dispose };
}
