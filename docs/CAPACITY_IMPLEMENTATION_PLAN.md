# Implementation backlog: 150 concurrent cmt-top users

Prepared 22 September 2026. Application changes for CAP-00 through CAP-07 are implemented on `capacity-150`. CAP-02 through CAP-06 have passed their local implementation checks. The replay harness, metrics and quotas are available; CAP-01/CAP-08 broader workload and deployment gates remain open. The task cards below retain their complete acceptance criteria and must not be read as a production-capacity certification. CAP-09 infrastructure and CAP-10 rollout remain pending. See [capacity testing](CAPACITY_TESTING.md) for executable commands and implemented limits.

Deliver 150 active browser sessions per environment, a successful 200-session burst, and correct investigations during a stalled block. Keep one collector/pod per environment. Treat 150 active plus 150 hidden tabs as a separate 300-connection test. Existing archive fidelity, authentication, public-access configuration and origin checks remain part of acceptance.

**Execution waves and ownership**

| Wave | Integration | Streaming and replay | Archive and infrastructure | Frontend |
| --- | --- | --- | --- | --- |
| 0 | CAP-00: freeze contracts, shared config and metrics interfaces | Review stream/fixture contract | Review report/revision contract | Review session/pause contract |
| 1, parallel | CAP-07: quota policy and shared HTTP wiring | CAP-01: executable replay and baseline | CAP-03: revisioned archive reads | CAP-05: transport and poller changes against contract fixtures |
| 2, parallel | Integrate routes/options, review ordering and telemetry | CAP-02: admission, shared encoding, bounded queues | CAP-04: report cache and compact API | CAP-06: compact investigation and lifecycle integration |
| 3, parallel | CAP-08: local integration, then representative tests after CAP-09a | CAP-08: load/profiling; fix owned stream paths | CAP-09a: test environment ready; CAP-09b: final sizing after CAP-08 | CAP-08: browser/compatibility tests; fix owned UI paths |
| 4 | CAP-10: staged release after gates pass | Confirm stream metrics | Confirm infrastructure and report metrics | Verify deployed browser behavior |

The three implementation tracks run in parallel with shared integration ownership. CAP-09a prepares schedulable test infrastructure and scrapes before representative CAP-08 measurements; CAP-09b selects final deployment settings afterward. Integrate shared wiring as each package lands.

Use one integration branch for app work, with exclusive file ownership. The Integration role owns shared files: `internal/config/config.go`, `cmd/cmt-top/main.go`, `internal/web/server.go`, metric definitions, `Makefile`, CI workflows, dependency manifests/lockfiles, configuration examples and top-level docs. Changes to these files require an explicit handoff with exact signatures or snippets. Keep infrastructure changes in their own repository branches. Keep shared checkouts stable while parallel work is active.

Every handoff includes changed files, checks run/results, before/after measurements where relevant, remaining risks and any shared-wiring request. A task is complete only when its acceptance checks pass and it is integrated. An independent reviewer checks concurrency-sensitive code before CAP-08 begins.

**CAP-00 contracts**

The implemented wire contracts are recorded in [capacity contracts](CAPACITY_CONTRACTS.md). Keep the Go/TypeScript shapes and checked-in JSON fixtures aligned when changing these interfaces.

| Surface | Chosen direction |
| --- | --- |
| Connection configuration | `ui.web.max_clients`, `CMTOP_WEB_MAX_CLIENTS`, and `web.Options.MaxClients`; initial application default 256, positive validated values. Omitted constructor options normalize to the documented default. Ship the higher cap only with the tested implementation. |
| Session/capabilities | Add small authenticated `GET /api/session`: schema version, server epoch, capability names and recommended cadence. Capacity information is advisory, not a slot reservation. Preserve 401; overload uses 429/503 plus `Retry-After`. A capability-endpoint 404 selects the legacy client path once, without a reload loop. |
| Existing stream | Preserve current envelope/event names, baseline-first ordering and per-client sequence semantics. Retain a full initial/resync baseline and at most one ordinary full snapshot per second in the first optimization. Keep the legacy default channel set. Add capability/epoch metadata to authoritative baselines; an absent marker identifies a legacy server, including on reconnect after rollback. |
| Investigation context stream | Advertise an opt-in `context-v1` capability/channel carrying chain identity/status (including catching-up), active/committed height, round, health, errors and upgrade state with explicit clear/removal semantics at a bounded cadence. Investigation clients subscribe to context and unsubscribe from dashboard channels after their initial baseline. Use the ordered subscription-command then ping/pong barrier for profile changes, followed by an authoritative resync/context baseline before applying patches. Re-negotiate the profile from every new connection's baseline. Older clients retain existing behavior. |
| Compact report | Preserve plain `/api/blocks/{height}/rounds`. Add `?view=compact&round=latest&compare=<n>`: all-round summaries plus at most two detailed rounds captured from one revision. Explicit integer selection stays pinned; absent comparison means none; missing/evicted selections have explicit states rather than silently selecting another round. Invalid parameters return 400. |
| Revision / HTTP caching | Use per-height evidence revision plus catalogue revision for retention and globally derived status. Include server epoch or content identity across restarts. Strong ETags cover the complete selected representation; 304 is valid only when that representation is unchanged. Volatile health/time is delivered separately through context. Compact responses can use private revalidation; existing full responses retain their contract. |
| Pause / export | Add capability-gated `?view=full&capture=1` for Pause/live export. It captures a complete snapshot after admission to the capture work budget and returns its actual revision/epoch and `capturedAt`; it cannot relabel an older coalesced response as a fresh capture. Legacy full reads use a separate bounded full-read lane under the same global builder/byte limits. Pausing an investigation captures one complete, consistent report, shows a pending state, then freezes that capture atomically. Paused selection/comparison/export use the local capture with no network. Failed capture keeps the prior usable state and shows failure. Label capture time accurately; this freezes the successfully captured revision, not an earlier screen revision. Full captures use a separate bounded work budget. |
| Telemetry / resource bounds | Integration defines injectable metrics hooks and bounded labels before implementation adds calls. Queue byte/age limits, report cache bytes and builder/export concurrency have explicit finite defaults, chosen from CAP-01 measurements and documented in tests. |

**Implementation task cards**

- [ ] **CAP-00 — Establish contracts, configuration and metrics plumbing. Owner: Integration. Dependency: none.**

  Own existing config/main/server/metrics files and their tests; add `docs/CAPACITY_CONTRACTS.md`, `internal/metrics/capacity.go` and `internal/web/session.go`. Implement configuration parsing, precedence, validation and constructor defaults. Specify the session response and register compatible additive routes/capabilities as their implementations become available; never advertise an unfinished capability. Define browser admission/active count, bytes/messages, queue age/bytes, resync, snapshot duration, API route/status/latency and report-cache metrics. Keep existing upstream/drop metrics distinct. Use injectable registries to avoid test registration collisions; HTTP observation wrappers must preserve WebSocket hijacking. Wire test-only/internal profiling without exposing it on the public app listener.

  **Done:** config precedence/invalid-value tests pass; metrics have no height/IP/hash/address labels; a real WebSocket upgrade succeeds through middleware; implementation owners agree on compile-ready interfaces and checked-in request/response fixtures.

- [ ] **CAP-01 — Build the reproducible workload and baseline. Owner: Testing. Dependency: CAP-00 contract.**

  Add `internal/testutil/replay/`, `cmd/cmt-top-replay/`, `cmd/cmt-top-load/`, `testdata/capacity/` and `deploy/capacity/compose.yaml`. Implement mock CometBFT HTTP/WebSocket and LCD surfaces needed by the real orchestrator: status, pinned/paginated validators, blocks, consensus fallback, staking metadata and upgrade plan. Drive subscriptions with seeded normal traffic, reconnect/outage and 1/32/128-round stalled-height scenarios, using 45- and 5-validator profiles plus 120-block history. Include nil, duplicate, conflicting, late and reordered observations, roster changes, commit and eviction. Build an independent expected-evidence oracle. Normalize wall-clock observation timestamps when comparing repeated runs, or inject a controlled clock; vote timestamps alone do not make the complete archive byte-identical.

  Run the real bundled app with all RPC/LCD/monitored endpoints pointing to the fixture and egress restricted to the test network. The driver models browser bootstrap, WS sequences/resync and investigation polling, with baseline and new-capability modes. Record the existing 32-client/60-per-IP failures as expected baseline results rather than making baseline CI fail. Capture payload bytes, CPU/allocations, archive read-lock time, upstream request counts and ingestion delay.

  **Done:** one reproducible smoke command, deterministic semantic evidence, zero external chain traffic, baseline artifacts and generator-utilization measurements. Do not use `make run` or the existing default Compose smoke as this fixture: they use live default endpoints.

- [x] **CAP-02 — Make streaming scale and remain bounded. Owner: Backend (streaming). Dependencies: CAP-00 and CAP-01 baseline.**

  Own `internal/web/ws.go`, `ws_test.go`; add `ws_wire.go`, `ws_queue.go` and matching tests. Replace the hard cap with configured `active + pending` admission. Reserve briefly, release locks for network upgrade, release failed reservations exactly once, then register and enqueue the authoritative baseline under ordering control. Handle shutdown during pending handshakes. Build/serialize common payloads once per broadcast; add only the per-client envelope without repeatedly scanning a large `json.RawMessage` through `WriteJSON`. Coalesce ordinary snapshots to one per second. Implement context subscriptions, finite queue bytes/age, superseded-snapshot handling, coalesced resync and persistent slow-reader disconnection.

  Document lock ordering and never perform network work while holding global locks. Close clients outside locks acquired by `close()`. Preserve monotonic per-client sequence and observable recovery after actual loss; replacing a queued snapshot must not apply an old baseline after newer patches. Keep one writer per connection.

  **Done:** 150 and 200 clients admitted at cap 256; the 257th rejected when 256 slots are occupied; failed/pending handshakes do not leak slots or stop delivery. Payload-encoding instrumentation demonstrates shared encoding at 1/100/150 viewers. Existing sequencing/subscription tests pass, slow clients remain isolated, and queue/resync bounds hold under `-race`.

- [x] **CAP-03 — Separate archive capture from expensive report construction. Owner: Backend (archive). Dependency: CAP-00 report contract.**

  Own `internal/divergence/archive.go`, `tracker.go`, archive tests; add detached-read/revision helpers and benchmarks in that package. Add per-height evidence revisions and a separate catalogue revision. Clone the needed raw data/revision under the tracker read lock; perform sorting, group/power calculation and JSON preparation after releasing it. Preserve existing `BlockInvestigation` results and copy independence.

  Cover every observable mutation: unique/late/conflicting votes, first truncation flags even when an observation is rejected, proposer changes, roster fill, round creation, commit/canonical hash and round eviction. Identical duplicates must not invalidate unchanged evidence. Global high-water marks and height eviction update status/retention without rebuilding every settled height's expensive evidence. Stamp a build with the revision actually captured, never a newer one observed later.

  **Done:** existing evidence fixtures match; readers/writers pass race checks; detached reads cannot mutate tracker state. Benchmarks for 1/32/128 rounds report lock-hold/build time and allocations; expensive materialization runs outside the ingestion lock.

- [x] **CAP-04 — Cache reports and serve compact investigations. Owner: Backend (archive). Dependencies: CAP-03 and CAP-00.**

  Own `internal/web/rounds.go`, `rounds_test.go`; add `rounds_cache.go` and focused cache/compact tests. Implement immutable materialization/serialized caches with byte limits, bounded builders and single-flight builds per height. Start active-height coalescing at 250 ms. Cache evidence separately from volatile context/catalogue metadata. Add the compact representation and strong ETag handling, validating selection parameters. Keep the plain full endpoint for older clients and implement the explicit full-capture mode, separating bounded legacy full-read and capture lanes from interactive requests. A capture can reuse immutable materialization only when it matches the revision freshly captured after admission; stamp the actual capture time. Abort a waiting request without corrupting a shared build. Never hold the cache mutex while acquiring the tracker lock or doing materialization/encoding.

  Recheck eviction/status when returning a cached view; bounded coalescing may serve an older correctly identified evidence revision, never mislabeled data. Bound variants across all height/round/comparison keys. Conditional headers and response cache policy must preserve auth/CORS behavior.

  **Done:** 150 simultaneous identical reads share one build; settled evidence does not rebuild on every poll; continuous changes create at most four builds/second per active height, excluding documented initial/capture behavior. Polling sends summaries and at most two round details. Late votes, truncation, commit, catalogue-only changes, epoch changes and eviction invalidate correctly. Full/compact evidence agrees with the oracle and cache/builder budgets hold.

- [x] **CAP-05 — Fix bootstrap, reconnect and polling demand. Owner: Frontend. Dependency: CAP-00; work against fixtures while APIs are being implemented.**

  Own `web/src/lib/api.ts`, `ws.ts`, `poll.ts` and their tests. Add typed status/304/retry results. Use the small session probe and WS baseline for new-server startup; retain a tested legacy fallback. Replace repeated full-state handshake probes with bounded lightweight checks. Reset backoff after a stable baseline/connection, add baseline timeout and one outstanding resync, and ignore callbacks from obsolete sockets. Keep authentication failures distinct from transient overload.

  Extend polling with dynamic intervals, jitter, `Retry-After`, bounded error backoff and independent hidden/manual-pause suspension. Preserve one in-flight request. Separate one-shot refresh from restarting the schedule so resume does not fetch twice. Treat a valid 304 as successful validation. Track desired subscriptions through reconnect and navigation. On every new socket, use authoritative baseline capabilities/epoch before applying a context-only profile; switch both stream subscriptions and report parser/API mode together on rollback. Cancel incompatible in-flight reads and detect a legacy full response rather than misparsing it as compact.

  **Done:** fake-clock/socket tests cover 401/429/503, flapping/missing-baseline sockets, malformed messages, sequence gaps, disposal races and capability 404. Successful new-protocol startup consumes exactly one full baseline; transient startup failure recovers automatically; hidden/paused pollers issue no automatic requests; resume performs one immediate refresh.

- [x] **CAP-06 — Integrate compact reads, visibility and exact paused evidence. Owner: Frontend. Dependencies: CAP-05 and CAP-04 contract; final integration requires CAP-02/CAP-04.**

  Own `App.svelte`, `components/BlockRounds.svelte`, `Header.svelte`, `lib/rounds.ts`, `types.ts`, `stores.ts`, `stores.test.ts` and related tests; add `lib/investigation.ts` and a testable session/lifecycle controller. Request selected/reference details together; cancel superseded height/selection work and reject mixed revisions/epochs. Avoid reparsing unchanged reports on 304. Active polls start near one second, settled polls at 5–10 seconds; tune against the end-to-end freshness gates. Derive stale indicators from cadence and last successful validation, separately from upstream freshness. Add the mandatory context reducer, including chain status and error/upgrade clears, so visible banners never depend on a frozen abandoned dashboard snapshot.

  Route investigation sockets to context updates; hidden tabs suspend app subscriptions/polling and use a bounded disconnect grace period. Returning visibility must not clear manual pause. Implement full-report capture for Pause/live export; retain the successful capture locally so paused browsing/export survives later votes and backend restart. Resume with one authoritative stream/context refresh plus one investigation refresh, then restart scheduling without a duplicate request. Keep existing report rendering/legacy fallback available.

  **Done:** request-count tests and browser checks confirm bounded traffic, no duplicate bootstrap/resume work, correct pending/error states, immutable paused evidence and preserved round/reference selection. Normal compact polling never downloads all detailed rounds. Export uses one complete consistent capture.

- [ ] **CAP-07 — Make HTTP quotas compatible with the audience. Owner: Integration. Dependencies: CAP-00; final tuning uses CAP-01/CAP-08.**

  Own `internal/web/server.go`; add a dedicated request-budget/trusted-proxy helper and tests. Verify the actual Gateway-to-app identity/header path. Accept forwarding information only from configured trusted proxy CIDRs and apply a documented trusted-hop rule. Give lightweight session probes, normal reads and expensive captures appropriate separate budgets. Preserve a global bound on report builders/export work even when per-client identities differ. Return structured 429/503 and retry guidance consumed by CAP-05. Account for 150 people sharing one NAT and synchronized startup; do not assume one source IP equals one user.

  **Done:** 150 normal sessions behind one proxy/NAT pass; spoofed forwarding headers do not escape limits; genuine excess work remains bounded. Auth/origin and conditional-request CORS tests pass. Initial API overload recovers automatically through the real frontend.

- [ ] **CAP-08 — Run integrated correctness and capacity gates. Owner: Integration; Testing and Frontend own load/browser evidence, Backend investigates archive failures. Dependencies: CAP-01 through CAP-07; representative deployment measurements also require CAP-09a.**

  Integration adds documented Make targets and a short CI smoke job plus an explicit long-running capacity job. Frontend adds `web/e2e/capacity.spec.ts` and browser-runner configuration; Integration owns its package/lockfile changes. Use a small real-browser cohort alongside 150 protocol sessions for repeatable checks, then a distributed 150-browser confirmation with load-generator saturation measured separately. Exercise legacy-client/new-server and new-client/rolled-back-server paths, including an already-open tab crossing a rollback. The 150-investigator capacity gate uses the compact-capable release client; legacy full-report compatibility has a separately bounded workload and may receive retry guidance under full-read limits. Do not imply capacity for 150 old full-report pollers. Capture exact commit/image, seed, offered traffic, hardware/limits, percentiles, errors, bytes, drops, GC and queue/cache bounds in `docs/capacity-results/<run-id>/` or linked CI artifacts.

  **Done:** the acceptance matrix below passes for both chain profiles, including mainnet-sized replay in the test environment. Tests distinguish overload deliberately generated outside the supported workload from errors affecting ordinary clients. No claim of 150-user capacity from admission tests alone.

- [ ] **CAP-09 — Prepare schedulable deployment and working scrapes. Owner: Infrastructure. Dependencies: CAP-00 config. CAP-09a does not wait for CAP-08; CAP-09b final resources do.**

  In [injective-helm-charts](https://github.com/InjectiveLabs/injective-helm-charts), update `charts/cmt-top/values.yaml`, `values.schema.json`, deployment template, environment overrides and render tests. Expose validated connection settings and scrape annotations; preserve one replica, `Recreate`, `kubernetes.io/ws` and HTTPRoute `request: 0s`. Start isolated sizing at requests 1 CPU/512 MiB and limits 2 CPU/1 GiB; choose final values from results.

  In [injective-iac](https://github.com/InjectiveLabs/injective-iac), update Asia/US SigNoz discovery to include `cmt-top`/`cmt-top-testnet` and retain node-local relabel filtering. Prepare per-application Argo revision pins. Recheck Asia's eligible-node reservations and arrange actual capacity before requesting 1 CPU; the September 22 review found no eligible Asia node with room. US has eligible alternative nodes, subject to recheck.

  **CAP-09a done before representative load:** Helm lint/schema/render tests pass, intended IaC diffs are reviewed, a performance pod can schedule with the starting resources, and each app pod is scraped exactly once. **CAP-09b done after CAP-08:** final resource/config values are chosen from the successful measurements and deployment renders are revalidated. Annotating undiscovered namespaces alone does not satisfy this task. Shared Gateway resource/availability changes remain a separately scoped follow-up unless load evidence requires them.

- [ ] **CAP-10 — Execute a reversible staged rollout. Owner: Integration with Infrastructure. Dependencies: CAP-08/CAP-09b.**

  Publish the tested versioned image and record its digest plus chart/config revisions. Deploy testnet using its own application `targetRevision`; explicitly retain mainnet's previous revision. The current shared Argo default controls both apps, so changing that first would defeat staging. Check fresh advancing observations, ingestion lag/drops, browser recovery, scrape targets and CPU/memory/queue metrics during an observation window. Export needed incident evidence, then roll out mainnet outside an active investigation and repeat checks.

  **Done:** both environments satisfy the same gates, with release evidence and previous image/chart/config recorded. Roll back on unexpected supported-load rejection, persistent freshness regression, growing queues/memory or ingestion loss. Rollback to the current release also restores its 32-connection cap: compatibility/retry behavior must work, but 150-user capacity is degraded until a fixed release is restored. Restart and rollback both clear the in-memory archive; exporting before restart is the preservation step, not a rollback restoration mechanism.

**Acceptance matrix**

| Test | Pass requirement |
| --- | --- |
| 1 → 32 → 50 → 100 → 150 sessions; 30-minute hold | No normal admission/quota rejection or unexpected disconnect; ordinary API error rate below 0.1%. |
| 200-session burst for five minutes | All 200 admitted at the proposed cap; healthy-client service and resource bounds maintained. Test beyond the configured cap separately for deliberate rejection. |
| 150 simultaneous browser cold starts/reconnects through one shared IP | At least 99% receive an authoritative baseline within ten seconds; include assets, session/bootstrap and browser state application. |
| 150 investigators: same/different heights, 1/32/128 rounds | Interactive API p95 <250 ms and p99 <1 s in-region; active-height evidence freshness from ingestion to displayed state p95 <2 s and p99 <3 s. Settled-height late evidence p99 <12 s with 5–10 s polling, or immediate explicit refresh; zero evidence mismatch. |
| Healthy clients plus slow/non-readers and malformed/resync traffic | Incremental delivery p95 <500 ms/p99 <1 s; snapshot-only metadata p99 <2 s; deliberate client loss repairs within two seconds; no global repair storm. |
| One-hour mixed soak and upstream outage/recovery | No ingestion/internal-bus drops under supported load, no sustained memory growth; RSS <70% of limit, CPU p95 <75% of limit, throttled periods <5%. |
| Pause/export burst, navigation and restart while paused | Finite capture concurrency/memory; explicit retry/busy handling; no partial/mixed-revision export; locally captured paused evidence survives restart. Capture latency is measured separately from interactive API latency. |
| 150 active plus 150 hidden tabs | Use a separately validated 512-connection budget if all 300 stay connected; hidden polling stops; visibility restores one baseline and preserves manual pause. |
| Upstream independence and bandwidth | Viewer count does not multiply collection/subscriptions; payload and wire bytes recorded; target 75% less dashboard streaming payload under identical replay. |

Retain the assessment's distinction between mandatory correctness/freshness gates and the 75% payload-reduction optimization target. If the first implementation passes mandatory gates but misses that target, record the actual network capacity/headroom and decide whether more stream work is necessary before release.

**Verification commands and deliverables**

Existing application checks, run from the repository root:

```sh
go test -mod=readonly -race -timeout=10m ./...
pnpm --dir web install --frozen-lockfile
pnpm --dir web check
pnpm --dir web test
pnpm --dir web build
go vet ./...
go test -mod=readonly -race -tags webui -timeout=10m ./...
go build -mod=readonly -tags webui -o bin/cmt-top ./cmd/cmt-top
```

The implemented capacity targets accept the shown parameters, run against isolated replay, collect artifacts and clean up on failure. These commands describe the full validation matrix; the completed runs and remaining gates are recorded in [capacity testing](CAPACITY_TESTING.md):

```sh
make capacity-smoke
make capacity-test PROFILE=mainnet45 USERS=150 HOLD=30m
make capacity-test PROFILE=stalled128 USERS=150 HOLD=30m
make capacity-test PROFILE=testnet5 USERS=150 HOLD=30m
make capacity-test PROFILE=mainnet45 USERS=200 HOLD=5m
make capacity-test PROFILE=mixed USERS=150 HOLD=1h
pnpm --dir web test:e2e
```

Run existing chart validation from the Helm checkout: `helm lint charts/cmt-top --set-string image.tag=validation-only`, lint with each environment override, and `python3 charts/cmt-top/tests/render_test.py`.

Keep code review units aligned with the packages: foundation/harness; streaming; archive/cache API; frontend; integration/capacity evidence; Helm/IaC rollout. Shared wiring can accompany its dependent package rather than creating a partially enabled feature. A complete implementation deliverable includes reproducible results, operational settings and a rollback record, not just passing unit tests.

**Parallel work packages and conditional follow-up**

After CAP-00, the independent implementation tracks are:

- **Testing and Backend (streaming):** implement CAP-01 in the replay/load paths. Exercise the real orchestrator with no chain egress, record the baseline and retain commands/results. Coordinate Make/CI changes with Integration. Continue to CAP-02 after the baseline is integrated.
- **Backend (archive):** implement CAP-03 in the divergence package. Preserve evidence semantics, add revisions/detached reads and prove invalidation/race behavior. Continue to CAP-04 against the frozen compact-report contract; coordinate shared server wiring with Integration.
- **Frontend:** implement CAP-05 in transport/polling files against the frozen fixtures. Prove bounded retries, one baseline and correct suspension. Continue to CAP-06, keeping App/BlockRounds lifecycle changes under one owner.

Open **CAP-X: batched stream/reducers** only if CAP-08 shows stream CPU/bandwidth or browser work still fails the agreed budget. Backend and Frontend then coordinate 100–250 ms batches and complete reducers for metadata, health/errors/clears, validator changes, upgrade removal, RPC comparison and block history before extending repair snapshots toward five seconds. Re-run the affected capacity/compatibility gates. A durable collector/store and multiple serving replicas are a later design milestone if an optimized pod cannot meet the target or restart availability becomes a requirement.
