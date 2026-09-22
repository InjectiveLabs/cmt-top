import { describe, expect, it } from "vitest";
import { get } from "svelte/store";
import {
  applySnapshot,
  dashboard,
  emptySnapshot,
  normalizeSnapshot,
  pauseView,
  pausedAt,
  reduceEvent,
  resumeView,
  view,
} from "./stores";
import type { Envelope, Snapshot, Validator } from "./types";

const validator = (address: string): Validator => ({
  address,
  index: 0,
  votingPower: "100",
  votingPowerPercent: 100,
  prevote: { kind: "absent", blockIDHash: "" },
  precommit: { kind: "absent", blockIDHash: "" },
  isProposer: false,
});
const state = (): Snapshot => ({
  ...emptySnapshot(),
  height: 12,
  committedHeight: 11,
  round: 1,
  step: 6,
  validators: [validator("A"), validator("B")],
});
const event = (type: string, payload: unknown): Envelope => ({ type, payload, seq: 1 });
const vote = (height = 12, round = 1, hash = "first", type = 1): Envelope =>
  event("vote.received", {
    Height: height,
    Round: round,
    ValidatorAddr: "A",
    BlockIDHash: hash,
    Type: type,
  });
describe("authoritative snapshot normalization", () => {
  it("clears removed metadata, upgrades, errors and validators", () => {
    const before = normalizeSnapshot({
      ...state(),
      upgrade: { name: "upgrade", height: 20 },
      errors: { rpc: "failed" },
      chain: { network: "example" },
    });
    const next = reduceEvent(
      before,
      event("state.snapshot", {
        height: 13,
        validators: [validator("C")],
        upgrade: null,
        errors: {},
      }),
      42,
    );
    expect(next.upgrade).toBeNull();
    expect(next.errors).toEqual({});
    expect(next.validators.map((v) => v.address)).toEqual(["C"]);
    expect(next.chain.network).toBe("");
    expect(next.receivedAt).toBe(42);
  });
  it("normalizes nullable history and sorts incidents newest first without duplicates", () => {
    const report = { Height: 11, Round: 0, Type: 2, IsDivergent: true };
    const s = normalizeSnapshot({
      divergence: { live: null, history: [report, { ...report, Height: 13 }, report] },
    });
    expect(s.divergence.live).toEqual([]);
    expect(s.divergence.history.map((r) => r.Height)).toEqual([13, 11]);
  });
  it("accepts server restart snapshots as new authority", () => {
    expect(
      reduceEvent(state(), event("state.snapshot", { height: 2, committedHeight: 1 })).height,
    ).toBe(2);
  });
});
describe("round and vote reducer", () => {
  it("rejects votes from committed heights and earlier rounds", () => {
    const before = state();
    expect(reduceEvent(before, vote(11))).toBe(before);
    expect(reduceEvent(before, vote(12, 0))).toBe(before);
  });
  it("rejects unknown vote types and validator identities", () => {
    const before = state();
    expect(reduceEvent(before, vote(12, 1, "block", 9))).toBe(before);
    expect(
      reduceEvent(
        before,
        event("vote.received", {
          Height: 12,
          Round: 1,
          ValidatorAddr: "X",
          Type: 1,
          BlockIDHash: "block",
        }),
      ),
    ).toBe(before);
  });
  it("advances on votes when NewRound is absent, atomically clearing previous votes", () => {
    const before = reduceEvent(state(), vote());
    before.validators[1].isProposer = true;
    const next = reduceEvent(before, vote(13, 0, "next", 2));
    expect([next.height, next.round, next.step]).toEqual([13, 0, 6]);
    expect(next.validators[0].prevote.kind).toBe("absent");
    expect(next.validators[0].precommit.blockIDHash).toBe("next");
    expect(next.validators[1].isProposer).toBe(false);
  });
  it("retains the first observation for a validator/type/context", () => {
    const before = reduceEvent(state(), vote());
    const next = reduceEvent(before, vote(12, 1, "different"));
    expect(next).toBe(before);
    expect(next.validators[0].prevote.blockIDHash).toBe("first");
    expect(reduceEvent(next, vote(12, 2, "different")).validators[0].prevote.blockIDHash).toBe(
      "different",
    );
  });
  it("does not regress a step or erase votes for a duplicate NewRound", () => {
    const before = reduceEvent(state(), vote());
    const next = reduceEvent(
      before,
      event("round.changed", { Height: 12, Round: 1, Step: 2, Proposer: "B" }),
    );
    expect(next.step).toBe(6);
    expect(next.validators[0].prevote.blockIDHash).toBe("first");
    expect(next.validators[1].isProposer).toBe(true);
  });
  it("resets to the next active height after a commit, never using the committed proposer", () => {
    const before = reduceEvent(state(), vote());
    const next = reduceEvent(before, event("block.committed", { Height: 12, ProposerAddr: "A" }));
    expect([next.height, next.committedHeight, next.round, next.step]).toEqual([13, 12, 0, 1]);
    expect(next.validators[0].prevote.kind).toBe("absent");
    expect(next.validators[0].isProposer).toBe(false);
  });
  it("preserves an already advanced active round when a commit arrives later", () => {
    const before = reduceEvent(state(), vote(13, 2));
    const next = reduceEvent(before, event("block.committed", { Height: 12 }));
    expect([next.height, next.round, next.step]).toEqual([13, 2, 4]);
    expect(next.validators[0].prevote.blockIDHash).toBe("first");
  });
  it("replaces a resolved incident by identity instead of adding duplicates", () => {
    const resolved = { Height: 12, Round: 1, Type: 2, Resolved: true, IsDivergent: true };
    const first = reduceEvent(
      state(),
      event("divergence.detected", { ...resolved, Resolved: false }),
    );
    const next = reduceEvent(
      reduceEvent(first, event("divergence.resolved", resolved)),
      event("divergence.resolved", resolved),
    );
    expect(next.divergence.live).toHaveLength(0);
    expect(next.divergence.history).toHaveLength(1);
  });
});
describe("frozen presentation", () => {
  it("retains the exact visible snapshot while collection continues, then resumes at the latest snapshot", () => {
    applySnapshot({ height: 12 });
    pauseView();
    applySnapshot({ height: 13 });
    expect(get(view).height).toBe(12);
    expect(get(dashboard).height).toBe(13);
    expect(get(pausedAt)).toBeGreaterThan(0);
    resumeView();
    expect(get(view).height).toBe(13);
    expect(get(pausedAt)).toBe(0);
  });
});

it("context snapshots clear diagnostics without erasing dashboard evidence", () => {
  const before = normalizeSnapshot({
    height: 10,
    chain: { network: "chain", catchingUp: true },
    errors: { rpc: "down" },
    upgrade: { name: "v2", height: 20 },
    validators: [{ address: "A", votingPower: "10" }],
    blocks: [{ height: 9 }],
  });
  const after = reduceEvent(before, {
    type: "context.snapshot",
    seq: 2,
    payload: {
      height: 11,
      committedHeight: 10,
      round: 1,
      step: 4,
      chain: { network: "chain", catchingUp: false },
      health: { mode: "streaming" },
      upgrade: null,
      errors: {},
    },
  });
  expect(after.height).toBe(11);
  expect(after.chain.catchingUp).toBe(false);
  expect(after.errors).toEqual({});
  expect(after.upgrade).toBeNull();
  expect(after.validators).toBe(before.validators);
  expect(after.blocks).toBe(before.blocks);
});
