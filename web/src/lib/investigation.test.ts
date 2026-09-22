import { afterEach, describe, expect, it, vi } from "vitest";
import { createInvestigationClient, normalizeCompact } from "./investigation";
import { legacySession } from "./session";
import { summarizePhase } from "./rounds";
const info = () => ({
  ...legacySession(),
  legacy: false,
  serverEpoch: "a",
  capabilities: ["rounds-compact-v1", "rounds-capture-v1"],
});
const detail = {
  round: 2,
  totalVotingPower: "10",
  validators: [
    {
      address: "A",
      votingPower: "10",
      knownToRoster: true,
      prevote: { observed: true, hashes: [{ hash: "aa" }] },
    },
  ],
};
const phase = {
  groups: [{ hash: "aa", votingPower: "10", validatorCount: 1 }],
  observedVotingPower: "10",
  observedValidatorCount: 1,
  notObservedVotingPower: "0",
  conflictingValidatorCount: 0,
};
const compact = {
  schemaVersion: 1,
  serverEpoch: "a",
  revision: "1:1",
  height: 7,
  status: "live",
  found: true,
  rounds: [
    { ...detail, validators: undefined, prevotes: phase, precommits: phase },
    { round: 3, totalVotingPower: "10", prevotes: phase, precommits: phase },
  ],
  details: [detail],
  selectedRound: 2,
  comparisonRound: null,
  selectionStatus: "available",
  comparisonStatus: "none",
};
const response = (value: unknown, etag = '"1"') =>
  new Response(JSON.stringify(value), { status: 200, headers: { ETag: etag } });
afterEach(() => vi.unstubAllGlobals());
describe("compact investigation and captures", () => {
  it("retains all summaries but only the requested detailed roster", () => {
    const value = normalizeCompact(compact, 7);
    expect(value.report.rounds).toHaveLength(2);
    expect(value.report.rounds[0].validators).toHaveLength(1);
    expect(value.report.rounds[1].validators).toHaveLength(0);
    expect(value.report.rounds[1].detailsLoaded).toBe(false);
    expect(summarizePhase(value.report.rounds[1], "prevote").observedPercent).toBe(100);
  });
  it("does not represent conflicting group powers as exclusive timeline segments", () => {
    const value = normalizeCompact(
      {
        ...compact,
        details: [],
        rounds: [
          {
            ...compact.rounds[0],
            prevotes: {
              ...phase,
              conflictingValidatorCount: 1,
              groups: [...phase.groups, { hash: "bb", votingPower: "10" }],
            },
          },
        ],
      },
      7,
    );
    const summary = summarizePhase(value.report.rounds[0], "prevote");
    expect(summary.segments.reduce((sum, segment) => sum + segment.percent, 0)).toBe(100);
    expect(summary.segments[0].label).toContain("conflicting");
  });
  it("reuses object identity for304 and isolates ETags by selection", async () => {
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce(response(compact))
      .mockResolvedValueOnce(new Response(null, { status: 304 }))
      .mockResolvedValueOnce(response(compact, '"2"'));
    vi.stubGlobal("fetch", fetcher);
    const client = createInvestigationClient(7, undefined, info),
      signal = new AbortController().signal;
    const first = await client.read({ round: 2 }, signal);
    expect(await client.read({ round: 2 }, signal)).toBe(first);
    expect(fetcher.mock.calls[1][1].headers["If-None-Match"]).toBe('"1"');
    await client.read({ round: 3, compare: 2 }, signal);
    expect(fetcher.mock.calls[2][1].headers["If-None-Match"]).toBeUndefined();
    expect(fetcher.mock.calls[2][0]).toContain("round=3&compare=2");
  });
  it("adapts to old servers ignoring compact query parameters", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response({ height: 7, rounds: [detail] })));
    const value = await createInvestigationClient(7, undefined, info).read(
      { round: "latest" },
      new AbortController().signal,
    );
    expect(value.compact).toBe(false);
    expect(value.report.rounds[0].validators).toHaveLength(1);
  });
  it("rejects responses from a different process epoch", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response({ ...compact, serverEpoch: "b" })));
    await expect(
      createInvestigationClient(7, undefined, info).read(
        { round: "latest" },
        new AbortController().signal,
      ),
    ).rejects.toThrow("restarted");
  });
  it("captures complete evidence once and leaves the local capture unchanged after restart", async () => {
    const full = {
      height: 7,
      schemaVersion: 1,
      serverEpoch: "a",
      revision: "3:4",
      capturedAt: "2026-09-22T00:00:00Z",
      rounds: [detail],
      context: { generatedAt: "2026-09-22T00:00:00Z" },
    };
    const fetcher = vi.fn().mockResolvedValue(response(full));
    vi.stubGlobal("fetch", fetcher);
    let session = info();
    const client = createInvestigationClient(7, undefined, () => session);
    const capture = await client.capture(new AbortController().signal),
      serialized = JSON.stringify(capture.raw);
    expect(fetcher.mock.calls[0][0]).toBe("/api/blocks/7/rounds?view=full&capture=1");
    session = { ...session, serverEpoch: "b" };
    expect(JSON.stringify(capture.raw)).toBe(serialized);
    expect(fetcher).toHaveBeenCalledOnce();
    expect(capture.compact).toBe(false);
    expect(capture.capturedAt).toBe(full.capturedAt);
  });
  it("refuses compact data or an unstamped response as a complete capture", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response(compact)));
    await expect(
      createInvestigationClient(7, undefined, info).capture(new AbortController().signal),
    ).rejects.toThrow("complete");
  });
});

it("preserves explicit unavailable selection states without substituting latest", () => {
  const value = normalizeCompact(
    {
      ...compact,
      selectedRound: null,
      selectionStatus: "evicted",
      comparisonStatus: "not_observed",
      details: [],
    },
    7,
  );
  expect(value.selectedRound).toBeNull();
  expect(value.selectionStatus).toBe("evicted");
  expect(value.comparisonStatus).toBe("not_observed");
  expect(value.report.rounds.every((round) => !round.detailsLoaded)).toBe(true);
});
