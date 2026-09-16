import { APIError } from "./api";

export interface PollOptions<T> {
  request: (signal: AbortSignal) => Promise<T>;
  onData: (data: T) => void;
  onError: (error: unknown) => void;
  onUnauthorized: () => void;
  onLoading?: (loading: boolean) => void;
  paused?: boolean;
  intervalMs?: number;
  timeoutMs?: number;
}

// The next attempt starts only after the previous request settles. Aborted or
// disposed requests cannot publish late results into another block's page.
export function createPoller<T>(options: PollOptions<T>) {
  let paused = options.paused ?? false;
  let disposed = false;
  let generation = 0;
  let active: AbortController | undefined;
  let timer: ReturnType<typeof setTimeout> | undefined;

  async function refresh(force = false): Promise<boolean> {
    if (disposed || (paused && !force) || active) return false;
    clearTimeout(timer);
    timer = undefined;
    const run = ++generation;
    const controller = new AbortController();
    active = controller;
    options.onLoading?.(true);
    const timeout = setTimeout(() => controller.abort(), options.timeoutMs ?? 8000);
    try {
      const data = await options.request(controller.signal);
      if (disposed || (paused && !force) || run !== generation || controller.signal.aborted)
        return false;
      options.onData(data);
      return true;
    } catch (error) {
      if (disposed || (paused && !force) || run !== generation) return false;
      if (error instanceof APIError && error.status === 401) {
        disposed = true;
        options.onUnauthorized();
      } else options.onError(error);
      return false;
    } finally {
      clearTimeout(timeout);
      if (active === controller) active = undefined;
      if (!disposed) options.onLoading?.(false);
      if (!disposed && !paused)
        timer = setTimeout(() => void refresh(), options.intervalMs ?? 1000);
    }
  }
  function setPaused(value: boolean) {
    if (paused === value || disposed) return;
    paused = value;
    clearTimeout(timer);
    if (value) {
      generation++;
      active?.abort();
      options.onLoading?.(false);
    } else void refresh();
  }
  function dispose() {
    disposed = true;
    generation++;
    clearTimeout(timer);
    active?.abort();
  }
  void refresh();
  return { refresh, setPaused, dispose };
}
