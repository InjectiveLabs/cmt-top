export interface SessionInfo {
  legacy: boolean;
  serverEpoch: string;
  capabilities: string[];
  cadence: { snapshotMs: number; activePollMs: number; settledPollMs: number };
}
export const legacySession = (): SessionInfo => ({
  legacy: true,
  serverEpoch: "",
  capabilities: [],
  cadence: { snapshotMs: 1000, activePollMs: 1000, settledPollMs: 5000 },
});
export function sessionFromBaseline(value: unknown): SessionInfo {
  const p = value as Record<string, unknown> | null;
  if (!p || p.schemaVersion === undefined) return legacySession();
  if (
    p.schemaVersion !== 1 ||
    typeof p.serverEpoch !== "string" ||
    !p.serverEpoch ||
    !Array.isArray(p.capabilities)
  )
    throw new Error(
      "This server uses an unsupported dashboard protocol. Refresh after the server update completes.",
    );
  const cadence = p.cadence as Record<string, unknown> | undefined;
  const interval = (key: string, fallback: number) =>
    typeof cadence?.[key] === "number" && Number.isFinite(cadence[key])
      ? Math.max(250, Math.min(30000, cadence[key]))
      : fallback;
  return {
    legacy: false,
    serverEpoch: p.serverEpoch,
    capabilities: p.capabilities.filter((v): v is string => typeof v === "string"),
    cadence: {
      snapshotMs: interval("snapshotMs", 1000),
      activePollMs: interval("activePollMs", 1000),
      settledPollMs: interval("settledPollMs", 5000),
    },
  };
}
