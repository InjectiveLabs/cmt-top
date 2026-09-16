import { describe, expect, it } from "vitest";
import {
  filterRoundValidators,
  hashColor,
  normalizeInvestigation,
  selectRound,
  summarizePhase,
  voteIdentity,
  type RecordedRound,
  type RecordedVote,
  type RoundValidator,
} from "./rounds";
const vote = (...hashes: string[]): RecordedVote => ({
  observed: hashes.length > 0,
  hashes: hashes.map((hash) => ({
    hash,
    firstSeenAt: "2026-09-16T00:00:00Z",
    lastSeenAt: "2026-09-16T00:00:00Z",
  })),
  conflicting: hashes.length > 1,
  truncated: false,
});
const validator = (
  address: string,
  power: string,
  prevote: RecordedVote,
  precommit = prevote,
): RoundValidator => ({
  address,
  votingPower: power,
  moniker: `Node ${address}`,
  knownToRoster: true,
  prevote,
  precommit,
});
const round = (validators: RoundValidator[], index = 0): RecordedRound => ({
  round: index,
  validators,
  proposer: "A",
  firstSeenAt: "",
  lastSeenAt: "",
  validatorRosterComplete: true,
  totalVotingPower: validators.reduce((sum, v) => sum + BigInt(v.votingPower), 0n).toString(),
  truncated: false,
});
const split = round([
  validator("A", "70", vote("aaa"), vote("bbb")),
  validator("B", "10", vote("bbb"), vote("aaa")),
  validator("C", "8", vote("")),
  validator("D", "5", vote(), vote("bbb")),
  validator("E", "4", vote("aaa")),
  validator("F", "3", vote("bbb"), vote()),
]);

describe("block round evidence model", () => {
  it("compares weighted cohorts and distinguishes nil from unobserved", () => {
    const pv = summarizePhase(split, "prevote"),
      pc = summarizePhase(split, "precommit");
    expect(pv.groups.map((g) => [g.hash, g.percent, g.members.map((v) => v.address)])).toEqual([
      ["aaa", 74, ["A", "E"]],
      ["bbb", 13, ["B", "F"]],
      ["", 8, ["C"]],
    ]);
    expect(pv.observedPercent).toBe(95);
    expect(pv.missing.map((v) => v.address)).toEqual(["D"]);
    expect(pc.groups[0].hash).toBe("bbb");
    expect(pc.groups[0].percent).toBe(75);
    expect(pv.hasQuorum).toBe(true);
    expect(voteIdentity(vote())).not.toBe(voteIdentity(vote("")));
  });
  it("counts a conflicting validator once in bars and suppresses quorum claims", () => {
    const summary = summarizePhase(
      round([
        validator("A", "70", vote("aaa", "bbb")),
        validator("B", "20", vote("aaa")),
        validator("C", "10", vote()),
      ]),
      "prevote",
    );
    expect(summary.groups.map((g) => g.percent)).toEqual([90, 70]);
    expect(summary.observedPercent).toBe(90);
    expect(summary.segments.reduce((sum, s) => sum + s.percent, 0)).toBe(100);
    expect(summary.segments.find((s) => s.key === "conflicts")?.percent).toBe(70);
    expect(summary.hasQuorum).toBe(false);
  });
  it("requires greater than two thirds, complete roster, and untruncated evidence for quorum", () => {
    expect(
      summarizePhase(
        round([validator("A", "2", vote("aaa")), validator("B", "1", vote())]),
        "prevote",
      ).hasQuorum,
    ).toBe(false);
    expect(summarizePhase({ ...split, validatorRosterComplete: false }, "prevote").hasQuorum).toBe(
      false,
    );
    expect(summarizePhase({ ...split, truncated: true }, "prevote").hasQuorum).toBe(false);
  });
  it("preserves power beyond floating point precision", () => {
    const summary = summarizePhase(
      round([
        validator("A", "900719925474099300", vote("aaa")),
        validator("B", "300239975158033100", vote("bbb")),
      ]),
      "prevote",
    );
    expect(summary.total).toBe(1200959900632132400n);
    expect(summary.groups[0].percent).toBe(75);
    expect(summary.hasQuorum).toBe(true);
  });
  it("filters phase changes, nil, missing, other hashes, and validator search", () => {
    const addresses = (filter: Parameters<typeof filterRoundValidators>[2]) =>
      filterRoundValidators(split, undefined, filter, "", null).map((v) => v.address);
    expect(addresses("phase_change")).toEqual(["A", "B"]);
    expect(addresses("nil")).toEqual(["C"]);
    expect(addresses("missing")).toEqual(["D", "F"]);
    expect(addresses("other_block")).toEqual(["B", "E", "F"]);
    expect(
      filterRoundValidators(split, undefined, "all", "node a", "bbb").map((v) => v.address),
    ).toEqual(["A"]);
    expect(filterRoundValidators(split, undefined, "all", "not here", null)).toEqual([]);
  });
  it("compares reference votes by identity across roster reorder", () => {
    const old = round(
      [...split.validators]
        .reverse()
        .map((v) => (v.address === "B" ? { ...v, prevote: vote("aaa") } : v)),
      1,
    );
    expect(filterRoundValidators(split, old, "changed", "", null).map((v) => v.address)).toEqual([
      "B",
    ]);
    expect(filterRoundValidators(split, undefined, "changed", "", null)).toEqual([]);
    expect(voteIdentity(vote("aaa", "bbb"))).toBe(voteIdentity(vote("bbb", "aaa")));
  });
  it("keeps a selected round pinned even when evicted, until follow latest is enabled", () => {
    const rounds = [split, round([], 1), round([], 2)];
    expect(selectRound(rounds, 0, false)).toBe(0);
    expect(selectRound(rounds, 0, true)).toBe(2);
    expect(selectRound(rounds.slice(1), 0, false)).toBe(0);
    expect(selectRound([], 0, true)).toBeNull();
    expect(hashColor("aaa")).toBe(hashColor("aaa"));
    expect(hashColor("aaa")).not.toBe(hashColor("bbb"));
  });
  it("preserves coverage and unavailable states without inventing historical votes", () => {
    for (const status of ["live", "passed", "committed", "not_observed", "evicted"]) {
      const report = normalizeInvestigation(
        {
          height: 100,
          status,
          found: false,
          rounds: null,
          coverage: { firstObservedRound: 3, missingRounds: 2, roundsEvicted: 1 },
          retention: { retainedHeights: [102, 101] },
          context: { activeHeight: 102 },
        },
        100,
      );
      expect(report.status).toBe(status);
      expect(report.rounds).toEqual([]);
      expect(report.coverage.firstObservedRound).toBe(3);
      expect(report.coverage.validatorRosterComplete).toBe(false);
      expect(report.retention.retainedHeights).toEqual([102, 101]);
    }
    expect(() => normalizeInvestigation({ height: 101 }, 100)).toThrow("different block height");
  });
});
