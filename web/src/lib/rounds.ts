import type { Health, Upgrade } from "./types";
import { normalizeSnapshot } from "./stores";
import { shortHash } from "./model";

export interface RecordedHash {
  hash: string;
  firstSeenAt: string;
  lastSeenAt: string;
  voteTimestamp?: string;
}
export interface RecordedVote {
  observed: boolean;
  hashes: RecordedHash[];
  conflicting: boolean;
  truncated: boolean;
}
export interface RoundValidator {
  address: string;
  moniker: string;
  votingPower: string;
  knownToRoster: boolean;
  prevote: RecordedVote;
  precommit: RecordedVote;
}
export interface RecordedRound {
  round: number;
  proposer: string;
  firstSeenAt: string;
  lastSeenAt: string;
  validatorRosterComplete: boolean;
  totalVotingPower: string;
  validators: RoundValidator[];
  truncated: boolean;
}
export interface BlockInvestigation {
  height: number;
  found: boolean;
  status: "live" | "committed" | "passed" | "not_observed" | "evicted";
  committed: boolean;
  canonicalHash: string;
  firstSeenAt: string;
  lastSeenAt: string;
  rounds: RecordedRound[];
  truncated: boolean;
  coverage: {
    firstObservedRound?: number;
    lastObservedRound?: number;
    roundsObserved: number;
    missingRounds: number;
    roundsEvicted: number;
    validatorRosterComplete: boolean;
  };
  retention: {
    heightLimit: number;
    roundLimit: number;
    hashesPerValidatorLimit: number;
    earliestHeight: number;
    latestHeight: number;
    retainedHeights: number[];
  };
  context: {
    chainId: string;
    activeHeight: number;
    activeRound: number;
    committedHeight: number;
    health: Health;
    upgrade: Upgrade | null;
    generatedAt: string;
    source: string;
  };
}
export type Phase = "prevote" | "precommit";
export type ComparisonFilter =
  | "all"
  | "phase_change"
  | "other_block"
  | "nil"
  | "missing"
  | "changed"
  | "conflicting";

type JsonObject = Record<string, unknown>;
const object = (v: unknown): JsonObject =>
  v && typeof v === "object" && !Array.isArray(v) ? (v as JsonObject) : {};
const array = (v: unknown): unknown[] => (Array.isArray(v) ? v : []);
const string = (v: unknown): string => (typeof v === "string" ? v : "");
const number = (v: unknown, fallback = 0): number =>
  typeof v === "number" && Number.isSafeInteger(v) && v >= 0 ? v : fallback;
const power = (v: unknown): string => (/^\d+$/.test(string(v)) ? string(v) : "0");
const hash = (v: unknown): string => string(v).toLowerCase();

function normalizeVote(value: unknown): RecordedVote {
  const v = object(value);
  const hashes = array(v.hashes).map((value) => {
    const h = object(value);
    return {
      hash: hash(h.hash),
      firstSeenAt: string(h.firstSeenAt),
      lastSeenAt: string(h.lastSeenAt),
      voteTimestamp: string(h.voteTimestamp),
    };
  });
  const unique = [...new Map(hashes.map((h) => [h.hash, h])).values()];
  return {
    observed: Boolean(v.observed) && unique.length > 0,
    hashes: unique,
    conflicting: Boolean(v.conflicting) || unique.length > 1,
    truncated: Boolean(v.truncated),
  };
}
export function normalizeInvestigation(value: unknown, expectedHeight: number): BlockInvestigation {
  const report = object(value);
  if (report.height !== expectedHeight)
    throw new Error("The server returned a different block height. Retry to load this block.");
  const coverage = object(report.coverage);
  const retention = object(report.retention);
  const context = object(report.context);
  const upgrade = object(context.upgrade);
  const rounds = array(report.rounds).map((value): RecordedRound => {
    const r = object(value);
    const validators = array(r.validators)
      .map((value): RoundValidator => {
        const v = object(value);
        return {
          address: string(v.address).toUpperCase(),
          moniker: string(v.moniker),
          votingPower: power(v.votingPower),
          knownToRoster: Boolean(v.knownToRoster),
          prevote: normalizeVote(v.prevote),
          precommit: normalizeVote(v.precommit),
        };
      })
      .filter((v) => Boolean(v.address));
    return {
      round: number(r.round),
      proposer: string(r.proposer).toUpperCase(),
      firstSeenAt: string(r.firstSeenAt),
      lastSeenAt: string(r.lastSeenAt),
      validatorRosterComplete: Boolean(r.validatorRosterComplete),
      totalVotingPower: power(r.totalVotingPower),
      validators: [...new Map(validators.map((v) => [v.address, v])).values()],
      truncated: Boolean(r.truncated),
    };
  });
  const status = report.status;
  return {
    height: expectedHeight,
    found: Boolean(report.found),
    status:
      status === "live" || status === "committed" || status === "passed" || status === "evicted"
        ? status
        : "not_observed",
    committed: Boolean(report.committed),
    canonicalHash: hash(report.canonicalHash),
    firstSeenAt: string(report.firstSeenAt),
    lastSeenAt: string(report.lastSeenAt),
    rounds: [...new Map(rounds.map((r) => [r.round, r])).values()].sort(
      (a, b) => a.round - b.round,
    ),
    truncated: Boolean(report.truncated),
    coverage: {
      firstObservedRound:
        typeof coverage.firstObservedRound === "number"
          ? number(coverage.firstObservedRound)
          : undefined,
      lastObservedRound:
        typeof coverage.lastObservedRound === "number"
          ? number(coverage.lastObservedRound)
          : undefined,
      roundsObserved: number(coverage.roundsObserved),
      missingRounds: number(coverage.missingRounds),
      roundsEvicted: number(coverage.roundsEvicted),
      validatorRosterComplete: Boolean(coverage.validatorRosterComplete),
    },
    retention: {
      heightLimit: number(retention.heightLimit),
      roundLimit: number(retention.roundLimit),
      hashesPerValidatorLimit: number(retention.hashesPerValidatorLimit),
      earliestHeight: number(retention.earliestHeight),
      latestHeight: number(retention.latestHeight),
      retainedHeights: array(retention.retainedHeights)
        .map((v) => number(v))
        .filter((h) => h > 0)
        .sort((a, b) => b - a),
    },
    context: {
      chainId: string(context.chainId),
      activeHeight: number(context.activeHeight),
      activeRound: number(context.activeRound),
      committedHeight: number(context.committedHeight),
      health: normalizeSnapshot({ health: context.health }).health,
      upgrade: context.upgrade
        ? { name: string(upgrade.name), height: number(upgrade.height) }
        : null,
      generatedAt: string(context.generatedAt),
      source: string(context.source),
    },
  };
}

export const powerOf = (value: string): bigint => {
  try {
    const n = BigInt(value);
    return n > 0n ? n : 0n;
  } catch {
    return 0n;
  }
};
export const percentOf = (value: bigint, total: bigint): number =>
  total ? Number((value * 1000000n) / total) / 10000 : 0;
export const hashLabel = (hash: string): string => (hash ? `Hash ${shortHash(hash)}` : "Nil");
// Hash-derived colors stay stable when groups change order or a new round arrives.
export function hashColor(hash: string): string {
  if (!hash) return "var(--muted)";
  let value = 2166136261;
  for (const c of hash) value = Math.imul(value ^ c.charCodeAt(0), 16777619);
  return `hsl(${(value >>> 0) % 360} 62% 57%)`;
}
export interface HashCohort {
  hash: string;
  power: bigint;
  percent: number;
  members: RoundValidator[];
}
export interface PhaseSummary {
  total: bigint;
  groups: HashCohort[];
  missing: RoundValidator[];
  conflicts: RoundValidator[];
  observedCount: number;
  observedPercent: number;
  missingPercent: number;
  leadingHash: string | undefined;
  hasQuorum: boolean;
  segments: { key: string; label: string; percent: number; color: string }[];
}
export function summarizePhase(round: RecordedRound, phase: Phase): PhaseSummary {
  const total = powerOf(round.totalVotingPower);
  const groups = new Map<string, HashCohort>();
  const exclusive = new Map<string, bigint>();
  const missing: RoundValidator[] = [],
    conflicts: RoundValidator[] = [];
  let observedPower = 0n,
    missingPower = 0n,
    conflictPower = 0n,
    observedCount = 0;
  for (const validator of round.validators) {
    const vote = validator[phase],
      power = powerOf(validator.votingPower);
    if (!vote.observed) {
      missing.push(validator);
      missingPower += power;
      continue;
    }
    observedPower += power;
    observedCount++;
    if (vote.conflicting) {
      conflicts.push(validator);
      conflictPower += power;
    }
    for (const observed of vote.hashes) {
      const group = groups.get(observed.hash) ?? {
        hash: observed.hash,
        power: 0n,
        percent: 0,
        members: [],
      };
      group.power += power;
      group.members.push(validator);
      groups.set(observed.hash, group);
      if (!vote.conflicting)
        exclusive.set(observed.hash, (exclusive.get(observed.hash) ?? 0n) + power);
    }
  }
  const ordered = [...groups.values()]
    .map((g) => ({ ...g, percent: percentOf(g.power, total) }))
    .sort((a, b) =>
      a.power === b.power ? a.hash.localeCompare(b.hash) : a.power > b.power ? -1 : 1,
    );
  const leading = ordered.find((g) => g.hash !== "");
  const segments = [...exclusive.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([hash, power]) => ({
      key: hash || "nil",
      label: hashLabel(hash),
      percent: percentOf(power, total),
      color: hashColor(hash),
    }));
  if (conflictPower)
    segments.push({
      key: "conflicts",
      label: "Conflicting observations",
      percent: percentOf(conflictPower, total),
      color: "var(--warn)",
    });
  if (missingPower)
    segments.push({
      key: "missing",
      label: "Not observed",
      percent: percentOf(missingPower, total),
      color: "var(--panel-2)",
    });
  return {
    total,
    groups: ordered,
    missing,
    conflicts,
    observedCount,
    observedPercent: percentOf(observedPower, total),
    missingPercent: percentOf(missingPower, total),
    leadingHash: leading?.hash,
    hasQuorum: Boolean(
      round.validatorRosterComplete &&
        !round.truncated &&
        !conflicts.length &&
        leading &&
        leading.power * 3n > total * 2n,
    ),
    segments,
  };
}
export const voteIdentity = (vote: RecordedVote | undefined): string =>
  !vote?.observed ? "unobserved" : JSON.stringify(vote.hashes.map((h) => h.hash).sort());
export function voteLabel(vote: RecordedVote | undefined): string {
  if (!vote?.observed) return "Not observed";
  return vote.hashes.map((h) => hashLabel(h.hash)).join(" + ");
}
export function filterRoundValidators(
  round: RecordedRound,
  reference: RecordedRound | undefined,
  filter: ComparisonFilter,
  search: string,
  selectedHash: string | null,
): RoundValidator[] {
  const query = search.trim().toLowerCase();
  const refs = new Map(reference?.validators.map((v) => [v.address, v]) ?? []);
  const leading = {
    prevote: summarizePhase(round, "prevote").leadingHash,
    precommit: summarizePhase(round, "precommit").leadingHash,
  };
  return round.validators
    .filter((v) => {
      if (query && !`${v.moniker} ${v.address}`.toLowerCase().includes(query)) return false;
      if (
        selectedHash !== null &&
        ![...v.prevote.hashes, ...v.precommit.hashes].some((h) => h.hash === selectedHash)
      )
        return false;
      const ref = refs.get(v.address);
      switch (filter) {
        case "phase_change":
          return (
            v.prevote.observed &&
            v.precommit.observed &&
            voteIdentity(v.prevote) !== voteIdentity(v.precommit)
          );
        case "other_block":
          return (["prevote", "precommit"] as Phase[]).some((phase) =>
            v[phase].hashes.some((h) => h.hash && h.hash !== leading[phase]),
          );
        case "nil":
          return [...v.prevote.hashes, ...v.precommit.hashes].some((h) => !h.hash);
        case "missing":
          return !v.prevote.observed || !v.precommit.observed;
        case "conflicting":
          return v.prevote.conflicting || v.precommit.conflicting;
        case "changed":
          return Boolean(
            ref &&
              (voteIdentity(v.prevote) !== voteIdentity(ref.prevote) ||
                voteIdentity(v.precommit) !== voteIdentity(ref.precommit)),
          );
        default:
          return true;
      }
    })
    .sort((a, b) =>
      powerOf(a.votingPower) === powerOf(b.votingPower)
        ? a.address.localeCompare(b.address)
        : powerOf(a.votingPower) > powerOf(b.votingPower)
          ? -1
          : 1,
    );
}
export function selectRound(
  rounds: RecordedRound[],
  selected: number | null,
  followLatest: boolean,
): number | null {
  if (!rounds.length) return null;
  if (followLatest || selected === null) return rounds[rounds.length - 1].round;
  return selected;
}
