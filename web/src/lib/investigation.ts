import { requestJSON } from "./api";
import {
  normalizeInvestigation,
  percentOf,
  powerOf,
  hashColor,
  hashLabel,
  type BlockInvestigation,
  type PhaseSummary,
  type RecordedRound,
} from "./rounds";
import type { SessionInfo } from "./session";
export interface InvestigationView {
  report: BlockInvestigation;
  raw: unknown;
  compact: boolean;
  selectedRound: number | null;
  selectionStatus: string;
  comparisonStatus: string;
  capturedAt?: string;
}
export interface InvestigationSelection {
  round: number | "latest";
  compare?: number;
}
const obj = (value: unknown): Record<string, any> =>
  value && typeof value === "object" ? (value as Record<string, any>) : {};
function phaseSummary(value: unknown, totalPower: string): PhaseSummary {
  const p = obj(value),
    total = powerOf(totalPower);
  const groups = (Array.isArray(p.groups) ? p.groups : []).map((g: any) => ({
    hash: String(g.hash || ""),
    power: powerOf(String(g.votingPower)),
    percent: percentOf(powerOf(String(g.votingPower)), total),
    members: [],
  }));
  const observed = percentOf(powerOf(String(p.observedVotingPower)), total),
    missing = percentOf(powerOf(String(p.notObservedVotingPower)), total);
  // Group powers overlap for equivocators. Without member-level evidence the
  // summary must not draw overlapping hash groups as exclusive percentages.
  const segments =
    Number(p.conflictingValidatorCount) > 0
      ? [
          {
            key: "observed",
            label: "Observed (includes conflicting votes)",
            percent: observed,
            color: "var(--warn)",
          },
        ]
      : groups.map((g: any) => ({
          key: g.hash || "nil",
          label: hashLabel(g.hash),
          percent: g.percent,
          color: hashColor(g.hash),
        }));
  if (missing)
    segments.push({
      key: "missing",
      label: "Not observed",
      percent: missing,
      color: "var(--panel-2)",
    });
  return {
    total,
    groups,
    missing: [],
    conflicts: [],
    observedCount: Number(p.observedValidatorCount) || 0,
    observedPercent: observed,
    missingPercent: missing,
    leadingHash: undefined,
    hasQuorum: false,
    segments,
  };
}
export function normalizeCompact(value: unknown, height: number): InvestigationView {
  const p = obj(value);
  if (
    p.schemaVersion !== 1 ||
    typeof p.serverEpoch !== "string" ||
    !p.serverEpoch ||
    typeof p.revision !== "string" ||
    !Array.isArray(p.details) ||
    !Array.isArray(p.rounds)
  )
    throw new Error("Invalid compact investigation response.");
  const report = normalizeInvestigation({ ...p, rounds: p.details }, height);
  const details = new Map(report.rounds.map((round) => [round.round, round]));
  report.rounds = p.rounds.map((summary: unknown): RecordedRound => {
    const s = obj(summary);
    const base = normalizeInvestigation({ height, rounds: [s] }, height).rounds[0];
    const detail = details.get(base.round);
    return {
      ...(detail || base),
      detailsLoaded: Boolean(detail),
      phaseSummaries: {
        prevote: phaseSummary(s.prevotes, base.totalVotingPower),
        precommit: phaseSummary(s.precommits, base.totalVotingPower),
      },
    };
  });
  return {
    report,
    raw: value,
    compact: true,
    selectedRound: Number.isSafeInteger(p.selectedRound) ? p.selectedRound : null,
    selectionStatus: String(p.selectionStatus || "not_observed"),
    comparisonStatus: String(p.comparisonStatus || "none"),
  };
}
function normalizeFull(value: unknown, height: number): InvestigationView {
  const p = obj(value),
    report = normalizeInvestigation(value, height);
  return {
    report,
    raw: value,
    compact: false,
    selectedRound: report.rounds[report.rounds.length - 1]?.round ?? null,
    selectionStatus: report.rounds.length ? "available" : "not_observed",
    comparisonStatus: "none",
    capturedAt: typeof p.capturedAt === "string" ? p.capturedAt : undefined,
  };
}
export function createInvestigationClient(
  height: number,
  token: string | undefined,
  session: () => SessionInfo,
) {
  let cached: { key: string; etag: string | null; value: InvestigationView } | undefined;
  async function read(
    selection: InvestigationSelection,
    signal: AbortSignal,
  ): Promise<InvestigationView> {
    const info = session(),
      compact = info.capabilities.includes("rounds-compact-v1");
    const query = new URLSearchParams();
    if (compact) {
      query.set("view", "compact");
      query.set("round", String(selection.round));
      if (selection.compare !== undefined) query.set("compare", String(selection.compare));
    }
    const path = `/api/blocks/${height}/rounds${query.size ? `?${query}` : ""}`;
    const key = `${info.serverEpoch}/${path}`;
    const response = await requestJSON(
      path,
      token,
      signal,
      cached?.key === key ? cached.etag || undefined : undefined,
    );
    if (session().serverEpoch !== info.serverEpoch)
      throw new Error("The server restarted. Refreshing evidence from its new observation window.");
    if (response.unchanged) {
      if (cached?.key !== key)
        throw new Error("The server returned unchanged evidence without a local baseline.");
      return cached.value;
    }
    const p = obj(response.data);
    if (p.serverEpoch && p.serverEpoch !== info.serverEpoch)
      throw new Error("The server restarted. Waiting for its new baseline.");
    const value =
      compact && p.schemaVersion === 1 && Array.isArray(p.details)
        ? normalizeCompact(p, height)
        : normalizeFull(p, height);
    cached = { key, etag: response.etag, value };
    return value;
  }
  async function capture(signal: AbortSignal): Promise<InvestigationView> {
    const info = session(),
      capable = info.capabilities.includes("rounds-capture-v1");
    const response = await requestJSON(
      `/api/blocks/${height}/rounds${capable ? "?view=full&capture=1" : ""}`,
      token,
      signal,
    );
    const p = obj(response.data);
    if (
      session().serverEpoch !== info.serverEpoch ||
      (p.serverEpoch && p.serverEpoch !== info.serverEpoch)
    )
      throw new Error("The server restarted during evidence capture. Retry the capture.");
    if (
      capable &&
      (p.schemaVersion !== 1 ||
        p.serverEpoch !== info.serverEpoch ||
        typeof p.revision !== "string" ||
        typeof p.capturedAt !== "string" ||
        !Number.isFinite(Date.parse(p.capturedAt)))
    )
      throw new Error("The server did not return a complete evidence capture.");
    if (Array.isArray(p.details))
      throw new Error("A compact response cannot be exported as complete evidence.");
    return normalizeFull(p, height);
  }
  return { read, capture };
}
