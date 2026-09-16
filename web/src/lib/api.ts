export class APIError extends Error {
  constructor(public status: number) {
    super(
      status === 401
        ? "Enter a valid access token to connect."
        : `Dashboard request failed (${status}).`,
    );
  }
}
export async function fetchSnapshot(token?: string, signal?: AbortSignal): Promise<unknown> {
  const headers: Record<string, string> = {};
  if (token) headers.Authorization = `Bearer ${token}`;
  const response = await fetch("/api/state", { headers, signal });
  if (!response.ok) throw new APIError(response.status);
  return response.json();
}

export async function fetchBlockRounds(
  height: number,
  token?: string,
  signal?: AbortSignal,
): Promise<unknown> {
  const headers: Record<string, string> = {};
  if (token) headers.Authorization = `Bearer ${token}`;
  const response = await fetch(`/api/blocks/${height}/rounds`, { headers, signal });
  if (!response.ok) throw new APIError(response.status);
  return response.json();
}
