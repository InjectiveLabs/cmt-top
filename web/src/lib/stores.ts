import { derived, get, writable } from "svelte/store";
import type { Divergence, DivergenceRound, Envelope, Snapshot, Validator, Vote } from "./types";
export type { Validator, DivergenceGroup, DivergenceRound, BlockSample } from "./types";

type RecordValue = Record<string, unknown>;
const record = (value: unknown): RecordValue =>
  value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as RecordValue)
    : {};
const num = (value: unknown, fallback = 0): number =>
  typeof value === "number" && Number.isFinite(value) ? value : fallback;
const str = (value: unknown, fallback = ""): string =>
  typeof value === "string" ? value : fallback;
const list = (value: unknown): unknown[] => (Array.isArray(value) ? value : []);
const strings = (value: unknown): string[] =>
  list(value).filter((v): v is string => typeof v === "string");
const absent = (): Vote => ({ kind: "absent", blockIDHash: "" });
function vote(value: unknown): Vote {
  const v = record(value);
  const kind = v.kind === "voted" || v.kind === "nil" || v.kind === "zero" ? v.kind : "absent";
  return { kind, blockIDHash: str(v.blockIDHash).toLowerCase() };
}
export function emptySnapshot(): Snapshot {
  return {
    height: 0,
    committedHeight: 0,
    round: 0,
    step: 0,
    blockTime: 0,
    activeRPC: "",
    chain: {},
    upgrade: null,
    validators: [],
    divergence: { live: [], history: [] },
    errors: {},
    blocks: [],
    explorerURL: "",
    receivedAt: 0,
    health: {
      mode: "unavailable",
      wsConnected: false,
      wsEndpoint: "",
      httpEndpoint: "",
      staleAfterMs: 15000,
    },
    rpcComparison: { status: "not_configured", height: 0, chainId: "", endpoints: [] },
  };
}
function normalizeRound(value: unknown): DivergenceRound {
  const r = record(value);
  return {
    Height: num(r.Height),
    Round: num(r.Round),
    Type: num(r.Type),
    TotalVotingPower: num(r.TotalVotingPower),
    TotalVotedPower: num(r.TotalVotedPower),
    Resolved: Boolean(r.Resolved),
    CanonicalHash: str(r.CanonicalHash),
    IsDivergent: Boolean(r.IsDivergent),
    Groups: list(r.Groups).map((value) => {
      const g = record(value);
      return {
        BlockIDHash: str(g.BlockIDHash),
        VotingPower: num(g.VotingPower),
        VotingPowerPct: num(g.VotingPowerPct),
        ValidatorCount: num(g.ValidatorCount),
        SampleMonikers: strings(g.SampleMonikers),
        Validators: strings(g.Validators),
        IsCanonical: Boolean(g.IsCanonical),
      };
    }),
  };
}
export const roundKey = (r: DivergenceRound): string => `${r.Height}/${r.Round}/${r.Type}`;
export function sortRounds(rounds: DivergenceRound[]): DivergenceRound[] {
  return [...new Map(rounds.map((r) => [roundKey(r), r])).values()]
    .sort((a, b) => b.Height - a.Height || b.Round - a.Round || b.Type - a.Type)
    .slice(0, 64);
}
function normalizeDivergence(value: unknown): Divergence {
  const d = record(value);
  return {
    live: sortRounds(list(d.live ?? d.Live).map(normalizeRound)),
    history: sortRounds(list(d.history ?? d.History).map(normalizeRound)),
  };
}
export function normalizeSnapshot(value: unknown, now = Date.now()): Snapshot {
  const s = record(value),
    h = record(s.health),
    c = record(s.chain),
    comparison = record(s.rpcComparison);
  const result = emptySnapshot();
  result.height = num(s.height);
  result.committedHeight = num(s.committedHeight);
  result.round = num(s.round);
  result.step = num(s.step);
  result.startTime = str(s.startTime);
  result.blockTime = num(s.blockTime);
  result.activeRPC = str(s.activeRPC);
  result.explorerURL = str(s.explorerURL);
  result.displayName = str(s.displayName);
  result.chain = {
    network: str(c.network),
    cometVersion: str(c.cometVersion),
    ourValidator: str(c.ourValidator).toUpperCase(),
    catchingUp: Boolean(c.catchingUp),
  };
  if (s.upgrade) {
    const u = record(s.upgrade);
    result.upgrade = { name: str(u.name), height: num(u.height) };
  }
  result.validators = list(s.validators)
    .map((value) => {
      const v = record(value);
      return {
        address: str(v.address).toUpperCase(),
        index: num(v.index),
        votingPower: str(v.votingPower, "0"),
        votingPowerPercent: num(v.votingPowerPercent),
        prevote: vote(v.prevote),
        precommit: vote(v.precommit),
        isProposer: Boolean(v.isProposer),
        operatorAddress: str(v.operatorAddress),
        moniker: str(v.moniker),
        jailed: Boolean(v.jailed),
        active: typeof v.active === "boolean" ? v.active : undefined,
        commissionRate: str(v.commissionRate),
      };
    })
    .filter((v) => v.address !== "");
  result.divergence = normalizeDivergence(s.divergence);
  result.errors = Object.fromEntries(
    Object.entries(record(s.errors)).filter(
      (entry): entry is [string, string] => typeof entry[1] === "string",
    ),
  );
  result.health = {
    mode:
      h.mode === "streaming" || h.mode === "polling" || h.mode === "stale" ? h.mode : "unavailable",
    wsConnected: Boolean(h.wsConnected),
    wsEndpoint: str(h.wsEndpoint),
    httpEndpoint: str(h.httpEndpoint),
    lastEventAt: str(h.lastEventAt),
    lastSuccessAt: str(h.lastSuccessAt),
    lastError: str(h.lastError),
    staleAfterMs: Math.max(1000, num(h.staleAfterMs, 15000)),
  };
  result.blocks = list(s.blocks)
    .map((value) => {
      const b = record(value);
      return {
        height: num(b.height),
        time: str(b.time),
        blockTimeMs: num(b.blockTimeMs),
        blockIDHash: str(b.blockIDHash),
        appHash: str(b.appHash),
        numTxs: num(b.numTxs),
      };
    })
    .filter((b) => b.height > 0)
    .sort((a, b) => a.height - b.height)
    .slice(-120);
  result.rpcComparison = {
    status:
      comparison.status === "matching" ||
      comparison.status === "mismatch" ||
      comparison.status === "incomplete"
        ? comparison.status
        : "not_configured",
    height: num(comparison.height),
    chainId: str(comparison.chainId),
    checkedAt: str(comparison.checkedAt),
    endpoints: list(comparison.endpoints).map((value) => {
      const e = record(value);
      return {
        endpoint: str(e.endpoint),
        status:
          e.status === "reference" ||
          e.status === "match" ||
          e.status === "mismatch" ||
          e.status === "lagging" ||
          e.status === "chain_mismatch"
            ? e.status
            : "error",
        chainId: str(e.chainId),
        height: num(e.height),
        latestHeight: num(e.latestHeight),
        appHash: str(e.appHash),
        error: str(e.error),
      };
    }),
  };
  result.receivedAt = now;
  return result;
}
function clearVotes(validators: Validator[]): Validator[] {
  return validators.map((v) => ({
    ...v,
    prevote: absent(),
    precommit: absent(),
    isProposer: false,
  }));
}
/** Snapshots are authoritative. Event patches only touch the active height/round. */
export function reduceEvent(state: Snapshot, env: Envelope, now = Date.now()): Snapshot {
  if (env.type === "state.snapshot") return normalizeSnapshot(env.payload, now);
  if (env.type === "context.snapshot") {
    const context = normalizeSnapshot(env.payload, now);
    return {
      ...state,
      height: context.height,
      committedHeight: context.committedHeight,
      round: context.round,
      step: context.step,
      chain: context.chain,
      health: context.health,
      upgrade: context.upgrade,
      errors: context.errors,
      displayName: context.displayName,
      receivedAt: now,
    };
  }
  const p = record(env.payload);
  const height = num(p.Height ?? p.height ?? env.height),
    round = num(p.Round ?? p.round ?? env.round);
  if (env.type === "vote.received") {
    const type = p.Type ?? p.type;
    if (type !== 1 && type !== 2 && type !== "prevote" && type !== "precommit") return state;
    if (
      height <= state.committedHeight ||
      height < state.height ||
      (height === state.height && round < state.round)
    )
      return state;
    const address = str(p.ValidatorAddr ?? p.validatorAddr).toUpperCase();
    if (!state.validators.some((v) => v.address === address)) return state;
    const advanced = height > state.height || round > state.round;
    const validators = advanced ? clearVotes(state.validators) : state.validators;
    const slot = type === 1 || type === "prevote" ? "prevote" : "precommit";
    // Match the collector's first-observation rule for duplicate/equivocating
    // votes. A snapshot can still correct this cell authoritatively.
    if (validators.find((v) => v.address === address)?.[slot].kind !== "absent") return state;
    const hash = str(p.BlockIDHash ?? p.blockIDHash).toLowerCase();
    const cell: Vote = { kind: hash ? "voted" : "nil", blockIDHash: hash };
    return {
      ...state,
      height,
      round,
      step: Math.max(advanced ? 0 : state.step, slot === "prevote" ? 4 : 6),
      validators: validators.map((v) => (v.address === address ? { ...v, [slot]: cell } : v)),
    };
  }
  if (env.type === "round.changed") {
    if (
      height <= state.committedHeight ||
      height < state.height ||
      (height === state.height && round < state.round)
    )
      return state;
    const advanced = height > state.height || round > state.round;
    const proposer = str(p.Proposer ?? p.proposer).toUpperCase();
    let validators = advanced ? clearVotes(state.validators) : state.validators;
    if (proposer)
      validators = validators.map((v) => ({ ...v, isProposer: v.address === proposer }));
    return {
      ...state,
      height,
      round,
      step: advanced ? num(p.Step ?? p.step) : Math.max(state.step, num(p.Step ?? p.step)),
      validators,
    };
  }
  if (env.type === "block.committed") {
    if (!height || height <= state.committedHeight) return state;
    const advanced = state.height <= height;
    return {
      ...state,
      committedHeight: height,
      ...(advanced
        ? { height: height + 1, round: 0, step: 1, validators: clearVotes(state.validators) }
        : {}),
    };
  }
  if (env.type === "divergence.snapshot")
    return { ...state, divergence: normalizeDivergence(env.payload) };
  if (env.type === "divergence.detected" || env.type === "divergence.resolved") {
    const report = normalizeRound(env.payload),
      key = roundKey(report);
    const d = state.divergence;
    return {
      ...state,
      divergence:
        env.type === "divergence.resolved"
          ? {
              live: d.live.filter((r) => roundKey(r) !== key),
              history: sortRounds([...d.history.filter((r) => roundKey(r) !== key), report]),
            }
          : { ...d, live: sortRounds([...d.live.filter((r) => roundKey(r) !== key), report]) },
    };
  }
  // Metadata and errors are replaced by the authoritative server snapshots.
  return state;
}

export const dashboard = writable<Snapshot>(emptySnapshot());
export const selectedValidator = writable<string | null>(null);
const frozen = writable<Snapshot | null>(null);
export const pausedAt = writable(0);
export const view = derived([dashboard, frozen], ([$dashboard, $frozen]) => $frozen ?? $dashboard);
export const chain = view;
export const validatorsOrdered = derived(view, (s) => s.validators);
export const divergence = derived(view, (s) => s.divergence);
export const blocks = derived(view, (s) => s.blocks);
export const errors = derived(view, (s) => s.errors);
export function applySnapshot(value: unknown): void {
  dashboard.set(normalizeSnapshot(value));
}
export function applyEnvelope(env: Envelope): void {
  dashboard.update((s) => reduceEvent(s, env));
}
export function pauseView(at = Date.now()): void {
  frozen.set(get(dashboard));
  pausedAt.set(at);
}
export function resumeView(): void {
  frozen.set(null);
  pausedAt.set(0);
}
