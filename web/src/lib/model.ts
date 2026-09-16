import type { Health, Validator } from "./types";
export const stepName = (step: number): string =>
  [
    "Awaiting step",
    "New height",
    "New round",
    "Propose",
    "Prevote",
    "Prevote wait",
    "Precommit",
    "Precommit wait",
    "Commit",
  ][step] ?? `Step ${step}`;
export const shortHash = (hash: string): string =>
  hash.length > 14 ? `${hash.slice(0, 8)}…${hash.slice(-6)}` : hash;
export const formatHeight = (height: number): string =>
  height > 0 ? height.toLocaleString() : "—";
export function elapsed(timestamp: string | number | undefined, now: number): string {
  const parsed = typeof timestamp === "number" ? timestamp : Date.parse(timestamp ?? "");
  if (!parsed || !Number.isFinite(parsed)) return "No data yet";
  const seconds = Math.max(0, Math.floor((now - parsed) / 1000));
  return seconds < 60
    ? `${seconds}s ago`
    : seconds < 3600
      ? `${Math.floor(seconds / 60)}m ago`
      : `${Math.floor(seconds / 3600)}h ago`;
}
export function healthMode(health: Health, now: number): Health["mode"] {
  if (health.mode === "unavailable") return "unavailable";
  const last = Date.parse(health.lastSuccessAt ?? "");
  if (!Number.isFinite(last)) return "unavailable";
  return now - last > health.staleAfterMs ? "stale" : health.mode;
}
export interface PowerGroup {
  hash: string;
  power: bigint;
  percent: number;
  count: number;
}
export interface ConsensusSummary {
  total: bigint;
  leading: PowerGroup | undefined;
  groups: PowerGroup[];
  nilPct: number;
  absentPct: number;
  observedPct: number;
  observedCount: number;
  totalCount: number;
  hasQuorum: boolean;
}
function powerOf(value: string): bigint {
  try {
    const n = BigInt(value);
    return n > 0n ? n : 0n;
  } catch {
    return 0n;
  }
}
const percentage = (n: bigint, total: bigint): number =>
  total > 0n ? Number((n * 1000000n) / total) / 10000 : 0;
export function consensusSummary(
  validators: Validator[],
  slot: "prevote" | "precommit",
): ConsensusSummary {
  let total = 0n,
    nil = 0n,
    absent = 0n,
    observedCount = 0;
  const groups = new Map<string, { power: bigint; count: number }>();
  for (const v of validators) {
    const power = powerOf(v.votingPower);
    total += power;
    const vote = v[slot];
    if (vote.kind === "voted" && vote.blockIDHash) {
      const group = groups.get(vote.blockIDHash) ?? { power: 0n, count: 0 };
      group.power += power;
      group.count++;
      groups.set(vote.blockIDHash, group);
      observedCount++;
    } else if (vote.kind === "nil") {
      nil += power;
      observedCount++;
    } else absent += power;
  }
  const ordered = [...groups.entries()]
    .map(([hash, group]) => ({ hash, ...group, percent: percentage(group.power, total) }))
    .sort((a, b) =>
      a.power === b.power ? a.hash.localeCompare(b.hash) : a.power > b.power ? -1 : 1,
    );
  return {
    total,
    leading: ordered[0],
    groups: ordered,
    nilPct: percentage(nil, total),
    absentPct: percentage(absent, total),
    observedPct: percentage(total - absent, total),
    observedCount,
    totalCount: validators.length,
    hasQuorum: Boolean(ordered[0] && ordered[0].power * 3n > total * 2n),
  };
}
export function explorerLink(template: string, validator: Validator): string | null {
  if (!template.includes("{address}")) return null;
  try {
    const url = new URL(
      template
        .split("{address}")
        .join(encodeURIComponent(validator.operatorAddress || validator.address)),
    );
    return url.protocol === "https:" || url.protocol === "http:" ? url.href : null;
  } catch {
    return null;
  }
}
