# Capacity implementation contracts

Implementation contract, 22 September 2026. Application code and fixtures must agree on these shapes. Existing full snapshot/report fields stay compatible.

**Session and baseline:** `GET /api/session` uses the existing API authentication and returns `{schemaVersion:1,serverEpoch:string,capabilities:string[],cadence:{snapshotMs:1000,activePollMs:1000,settledPollMs:5000}}`. Authoritative `state.snapshot` payloads contain the same `schemaVersion`, `serverEpoch`, `capabilities`, and `cadence` fields in addition to existing fields. A missing baseline marker identifies a legacy server on every connection, even if an earlier session probe succeeded. Capability names are `context-v1`, `rounds-compact-v1`, and `rounds-capture-v1`; advertise only implemented features.

**WebSocket:** preserve `{type,ts,height?,round?,seq,payload}` and the initial full baseline. Existing default subscriptions stay unchanged. Add channel `context` and message `context.snapshot`, containing `{height,committedHeight,round,step,chain,health,upgrade,errors,displayName,serverEpoch}`; clear fields explicitly. A context-only client sends subscribe(context), unsubscribe(state,blocks,divergence,votes), ping. Ordered pong is the subscription barrier. `resync` always returns a full authoritative state.snapshot; context snapshots reconcile every second. Do not treat context.snapshot as a full dashboard snapshot. Each connection/profile change preserves sequence-gap semantics. Restoration to dashboard subscribes its channels, unsubscribes context, then requests full resync.

**Configuration:** `ui.web.max_clients` / `CMTOP_WEB_MAX_CLIENTS` / `web.Options.MaxClients`; default 256; configured values must be positive. Existing constructors omitting the option use the default. HTTP proxy/rate policy is separate and bounded; no untrusted forwarding headers.

**Compact investigation:** `GET /api/blocks/{height}/rounds?view=compact&round=latest&compare=N`. `round` defaults to latest; integers are nonnegative and comparison is optional. Return:

```json
{
  "schemaVersion": 1,
  "serverEpoch": "process-unique",
  "revision": "evidence-revision:catalogue-revision",
  "height": 1001,
  "found": true,
  "status": "live",
  "committed": false,
  "canonicalHash": "",
  "firstSeenAt": "2026-09-22T00:00:00Z",
  "lastSeenAt": "2026-09-22T00:00:01Z",
  "truncated": false,
  "coverage": {},
  "retention": {},
  "rounds": [],
  "details": [],
  "selectedRound": 2,
  "comparisonRound": null,
  "selectionStatus": "available",
  "comparisonStatus": "none"
}
```

`coverage` and `retention` retain existing full-report shapes. `rounds` contains summaries with `round`, `proposer`, `firstSeenAt`, `lastSeenAt`, `validatorRosterComplete`, `totalVotingPower`, `truncated`, and `prevotes`/`precommits`. Summary phases contain groups `{hash,votingPower,validatorCount,isCanonical}` plus `observedVotingPower`, `observedValidatorCount`, `notObservedVotingPower`, `notObservedValidatorCount`, and `conflictingValidatorCount`; omit member arrays. `details` contains at most two distinct existing full `InvestigationRound` values. Missing selection is null with `not_observed` or `evicted` status; no silent replacement of an explicit round. `comparisonStatus` also allows `none`. Empty archives have empty arrays. Numeric request parse errors return 400. Full and compact representation types must be distinguished by schema/shape, as old servers ignore unknown query parameters.

No volatile health or request-time `generatedAt` belongs in compact bodies. Context comes from the live stream. Strong ETag covers the entire selected representation including status/retention and process identity. Support If-None-Match/304 and private revalidation. A 304 advances validation time without normalizing an unchanged object again.

**Capture:** `?view=full&capture=1` returns the existing full-report shape/context plus `{schemaVersion:1,serverEpoch,revision,capturedAt}`. Snapshot after bounded capture admission; accurately identify that captured revision/time, never relabel an older coalesced copy. Legacy plain full reads keep existing shape, with a separate bounded lane. Capture is explicit for Pause/live export. Once paused, use the complete local capture for selections/export until resume/navigation. On failed capture retain the prior usable UI state; show a pending/error state. Resume refreshes once before restarting the schedule.

**Versions/retention:** per-height evidence revision changes for every observable archive mutation, including truncation flags, roster fills, late votes and commit evidence. Catalogue revision tracks global status/retention. Build outside the tracker lock from detached data and stamp the revision actually copied. Bound cache bytes, flights, variants and full-read/capture concurrency. Never return cached retained data after eviction. Duplicate observations that do not change output do not change revision.

**Recovery:** small session probes replace full-state handshake probes on capable servers. Retry transient 429/503 with Retry-After, jitter and bounded exponential backoff. Re-negotiate from each authoritative baseline before restoring context-only subscriptions; rollback changes both report parser/API mode and stream profile. A legacy fallback preserves functionality but does not claim 150-user capacity on the old 32-connection server.

**Timing:** ordinary full/context snapshots at most once per second, fresh initial/resync baselines, coalesced resync. Active report polling approximately one second, settled polling five seconds with jitter, hidden/manual-paused polls suspended. Active evidence p95 <2 s/p99 <3 s; settled late evidence p99 <12 s. Pause capture/export has a separate concurrency/latency budget.

**Ownership for implementation:** Integration owns config/main/server wiring, metric definitions, Make/CI/dependencies/docs. Backend owns `internal/web/ws*`, `internal/divergence` and `internal/web/rounds*`. Frontend owns `web/src` and the browser tests. Testing owns replay/load/test harness paths and independent integration tests. Shared-file edits require an explicit handoff.
