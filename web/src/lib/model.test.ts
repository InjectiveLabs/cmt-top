import { describe, expect, it } from "vitest";
import { consensusSummary, explorerLink, healthMode } from "./model";
import { emptySnapshot } from "./stores";
import type { Validator, VoteKind } from "./types";
function validator(
  address: string,
  power: string,
  hash = "",
  kind: VoteKind = "absent",
): Validator {
  return {
    address,
    index: 0,
    votingPower: power,
    votingPowerPercent: 0,
    isProposer: false,
    prevote: { kind, blockIDHash: hash },
    precommit: { kind, blockIDHash: hash },
  };
}
describe("weighted consensus", () => {
  it("uses voting power rather than validator count", () => {
    const s = consensusSummary(
      [validator("A", "70", "block", "voted"), validator("B", "20"), validator("C", "10")],
      "precommit",
    );
    expect(s.leading?.percent).toBe(70);
    expect(s.observedCount).toBe(1);
    expect(s.totalCount).toBe(3);
    expect(s.hasQuorum).toBe(true);
    expect(s.absentPct).toBe(30);
  });
  it("does not combine different BlockIDs into a quorum", () => {
    const s = consensusSummary(
      [
        validator("A", "40", "one", "voted"),
        validator("B", "30", "two", "voted"),
        validator("C", "30"),
      ],
      "precommit",
    );
    expect(s.observedPct).toBe(70);
    expect(s.leading?.percent).toBe(40);
    expect(s.hasQuorum).toBe(false);
  });
  it("keeps nil votes outside BlockID groups", () => {
    const s = consensusSummary(
      [validator("A", "70", "", "nil"), validator("B", "30", "block", "voted")],
      "prevote",
    );
    expect(s.nilPct).toBe(70);
    expect(s.groups).toHaveLength(1);
    expect(s.leading?.percent).toBe(30);
    expect(s.hasQuorum).toBe(false);
  });
  it("requires strictly more than two thirds, including large integer power", () => {
    expect(
      consensusSummary(
        [
          validator("A", "200000000000000000000", "block", "voted"),
          validator("B", "100000000000000000000"),
        ],
        "precommit",
      ).hasQuorum,
    ).toBe(false);
    expect(
      consensusSummary(
        [
          validator("A", "200000000000000000001", "block", "voted"),
          validator("B", "100000000000000000000"),
        ],
        "precommit",
      ).hasQuorum,
    ).toBe(true);
  });
  it("handles unavailable or malformed power without NaN", () => {
    const s = consensusSummary([validator("A", "invalid")], "prevote");
    expect(s.observedPct).toBe(0);
    expect(s.hasQuorum).toBe(false);
    expect(s.total).toBe(0n);
  });
});
describe("truthful metadata", () => {
  it("ages previously healthy RPC data to stale without another message", () => {
    const health = {
      ...emptySnapshot().health,
      mode: "streaming" as const,
      lastSuccessAt: "2026-09-16T00:00:00Z",
      staleAfterMs: 15000,
    };
    expect(healthMode(health, Date.parse("2026-09-16T00:00:10Z"))).toBe("streaming");
    expect(healthMode(health, Date.parse("2026-09-16T00:00:16Z"))).toBe("stale");
  });
  it("treats missing success timestamps as unavailable", () => {
    expect(healthMode({ ...emptySnapshot().health, mode: "streaming" }, Date.now())).toBe(
      "unavailable",
    );
  });
  it("only links configured HTTP explorers, substituting the operator identity safely", () => {
    const v = { ...validator("AA", "1"), operatorAddress: "operator/address" };
    expect(explorerLink("https://explorer.example/validator/{address}", v)).toBe(
      "https://explorer.example/validator/operator%2Faddress",
    );
    expect(explorerLink("javascript:alert('{address}')", v)).toBeNull();
    expect(explorerLink("https://example.org", v)).toBeNull();
  });
});
