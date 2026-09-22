export class APIError extends Error {
  constructor(
    public status: number,
    public retryAfterMs = 0,
  ) {
    super(
      status === 401
        ? "Enter a valid access token to connect."
        : status === 429 || status === 503
          ? "The dashboard is busy. Retrying shortly."
          : `Dashboard request failed (${status}).`,
    );
  }
}
export function retryAfter(value: string | null, now = Date.now()): number {
  if (!value) return 0;
  const seconds = Number(value);
  return Math.max(0, Number.isFinite(seconds) ? seconds * 1000 : (Date.parse(value) || now) - now);
}
export interface APIResult {
  data?: unknown;
  etag: string | null;
  unchanged: boolean;
}
export async function requestJSON(
  path: string,
  token?: string,
  signal?: AbortSignal,
  etag?: string,
): Promise<APIResult> {
  const headers: Record<string, string> = {};
  if (token) headers.Authorization = `Bearer ${token}`;
  if (etag) headers["If-None-Match"] = etag;
  const response = await fetch(path, { headers, signal });
  if (response.status === 304)
    return { unchanged: true, etag: response.headers.get("ETag") || etag || null };
  if (!response.ok)
    throw new APIError(response.status, retryAfter(response.headers.get("Retry-After")));
  return { unchanged: false, data: await response.json(), etag: response.headers.get("ETag") };
}
export async function fetchSession(token?: string, signal?: AbortSignal): Promise<unknown> {
  try {
    return (await requestJSON("/api/session", token, signal)).data;
  } catch (error) {
    if (!(error instanceof APIError) || error.status !== 404) throw error;
    await requestJSON("/api/version", token, signal);
    return null;
  }
}
export async function fetchSnapshot(token?: string, signal?: AbortSignal): Promise<unknown> {
  return (await requestJSON("/api/state", token, signal)).data;
}
export async function fetchBlockRounds(
  height: number,
  token?: string,
  signal?: AbortSignal,
): Promise<unknown> {
  return (await requestJSON(`/api/blocks/${height}/rounds`, token, signal)).data;
}
