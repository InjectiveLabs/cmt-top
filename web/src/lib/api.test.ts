import { afterEach, describe, expect, it, vi } from "vitest";
import { APIError, fetchSession, requestJSON, retryAfter } from "./api";
afterEach(() => vi.unstubAllGlobals());
describe("conditional API transport", () => {
  it("keeps authentication and handles304 without reading JSON", async () => {
    const json = vi.fn(),
      fetcher = vi.fn().mockResolvedValue({ status: 304, ok: false, headers: new Headers(), json });
    vi.stubGlobal("fetch", fetcher);
    expect(await requestJSON("/api/blocks/1/rounds", "secret", undefined, '"revision"')).toEqual({
      unchanged: true,
      etag: '"revision"',
    });
    expect(fetcher.mock.calls[0][1].headers).toEqual({
      Authorization: "Bearer secret",
      "If-None-Match": '"revision"',
    });
    expect(json).not.toHaveBeenCalled();
  });
  it("preserves retry guidance on overload", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValue(new Response("busy", { status: 429, headers: { "Retry-After": "3" } })),
    );
    await expect(requestJSON("/api/session")).rejects.toMatchObject({
      status: 429,
      retryAfterMs: 3000,
    });
    expect(retryAfter("Tue, 22 Sep 2026 00:00:05 GMT", Date.parse("2026-09-22T00:00:00Z"))).toBe(
      5000,
    );
    expect(retryAfter("nonsense")).toBe(0);
  });
  it("uses only small endpoints for a legacy authentication probe", async () => {
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce(new Response("missing", { status: 404 }))
      .mockResolvedValueOnce(new Response("{}", { status: 200 }));
    vi.stubGlobal("fetch", fetcher);
    expect(await fetchSession()).toBeNull();
    expect(fetcher.mock.calls.map(([url]) => url)).toEqual(["/api/session", "/api/version"]);
  });
  it("does not disguise authentication failure as legacy fallback", async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response("unauthorized", { status: 401 }));
    vi.stubGlobal("fetch", fetcher);
    await expect(fetchSession()).rejects.toBeInstanceOf(APIError);
    expect(fetcher).toHaveBeenCalledOnce();
  });
});
