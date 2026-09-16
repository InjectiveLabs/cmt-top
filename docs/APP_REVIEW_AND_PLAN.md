# Historical app review and improvement plan

Reviewed 16 September 2026 at commit `2ae3404`. This is a historical assessment and implementation plan, not a description of the current release. See [implementation results](IMPLEMENTATION_RESULTS.md) for the changes and verification completed after this review.

## Assessment

cmt-top is a working early operator dashboard with a compact architecture and a lightweight web interface. It successfully connects to the default Injective RPC, enriches validator names, streams votes, and supports search. Its strongest foundation is the separation between transport, state, divergence tracking, and presentation.

The next release should concentrate on trustworthy monitoring. A dashboard that looks healthy while its upstream data is unavailable, mixes votes between rounds or validators, or describes BlockID differences as AppHash mismatches can mislead operators. These issues take priority over adding more panels. Retain Go, Svelte, the embedded SPA, and the dense operator-oriented design; a framework rewrite is not justified by this review.

Assumed audience: node operators and engineers watching one configured chain, investigating validator participation and consensus incidents. Fleet management, accounts, and trading features are outside this plan.

## What was verified

| Check | Result |
| --- | --- |
| Frozen frontend install and production build | Passed: `pnpm install --frozen-lockfile && pnpm build`. JavaScript bundle about 29 KB / 10.7 KB gzip. |
| Go race tests | `go test -race ./...` passed after generating the embedded web assets. |
| Go static checks | `go vet ./...` passed. |
| Test breadth | Ten tests, all in the divergence tracker. Passing race tests do not validate unexercised streaming, TUI, or orchestration paths. |
| Live web smoke test | Default Injective configuration loaded 45 validators and advancing heights. Search for `Kraken` returned two matching rows. No captured browser warnings/errors in this session. |
| No-results search | An unmatched query produced an empty table and `0/45`, without explanatory text or a clear-search action. |
| Desktop layout | Fixed side panels constrained the table; long monikers and operator addresses were clipped or required horizontal movement. |
| Mobile layout | At 390 × 844, the grid overflowed horizontally and the main validator table was off-screen. |
| Unavailable upstream | A separate local instance pointed at an unreachable loopback RPC showed green `open`, height zero, empty validators, and raw connection errors. |

Docker startup, TUI interaction failures, the subscription-map race, and hanging-RPC behavior were reviewed from source rather than reproduced end-to-end. No sustained load test, chain compatibility matrix, authentication regression suite, or external security audit was performed. All runtime checks used local dashboard listeners; public RPC access was read-only.

## Findings and priority

P1 means address before relying on the next release for unattended monitoring. P2 means the next usability/reliability increment. Source locations refer to the reviewed commit.

| Priority | Finding and consequence | Evidence | Proposed correction |
| --- | --- | --- | --- |
| P1 | Browser connection status is presented as overall health; upstream failure can still show green `open`. Errors and metadata can remain stale. | [Header.svelte](https://github.com/InjectiveLabs/cmt-top/blob/2ae3404/web/src/components/Header.svelte#L27), [App.svelte](https://github.com/InjectiveLabs/cmt-top/blob/2ae3404/web/src/App.svelte#L29), [snapshot serializer](https://github.com/InjectiveLabs/cmt-top/blob/2ae3404/internal/web/server.go#L362). Reproduced with an unreachable RPC. | Model browser feed, RPC transport, and last successful data receipt separately. Show streaming, polling, stale, and unavailable states with timestamps. |
| P1 | Web ignores status, upgrade, block-time, validator-set, and upstream connection events that the server emits. | [event mapping](https://github.com/InjectiveLabs/cmt-top/blob/2ae3404/internal/web/ws.go#L178), [client dispatch](https://github.com/InjectiveLabs/cmt-top/blob/2ae3404/web/src/App.svelte#L29). | Define typed event payloads and apply updates or explicit snapshot invalidation. Include error clearing and upgrade removal, not just successful additions. |
| P1 | Late votes can repaint a closed round in the web UI; block, round, vote, and fallback transitions do not share a consistent height/round model. | [web vote reducer](https://github.com/InjectiveLabs/cmt-top/blob/2ae3404/web/src/lib/stores.ts#L80), [backend stale guard and later publication](https://github.com/InjectiveLabs/cmt-top/blob/2ae3404/internal/core/orchestrator.go#L367), [block rollover](https://github.com/InjectiveLabs/cmt-top/blob/2ae3404/internal/core/orchestrator.go#L241). | Separate committed height from active consensus height. Apply monotonic height/round transitions, reject stale inputs, and reset all associated round fields atomically. |
| P1 | A same-size validator refresh can preserve votes by row index while replacing validator identity. | [refresh merge](https://github.com/InjectiveLabs/cmt-top/blob/2ae3404/internal/core/orchestrator.go#L515), [validator pagination](https://github.com/InjectiveLabs/cmt-top/blob/2ae3404/internal/chain/cometrpc/client.go#L141). | Merge by consensus address, retain votes only for the matching round, update total voting power, and pin all pages to one validator-set height. |
| P1 | Nil-majority rounds can be labeled divergent with only one actual BlockID. Detection events can also count one incident repeatedly. | [group ordering/detection](https://github.com/InjectiveLabs/cmt-top/blob/2ae3404/internal/divergence/tracker.go#L277), [counter increment](https://github.com/InjectiveLabs/cmt-top/blob/2ae3404/internal/metrics/metrics.go#L95). | Choose the leading non-nil group; require distinct non-nil BlockIDs for a split. Separate incident creation from updates and define resolution semantics. |
| P1 | Consensus bars show percentage of validator count, which is not a voting-power quorum measure. | [ChainCard.svelte](https://github.com/InjectiveLabs/cmt-top/blob/2ae3404/web/src/components/ChainCard.svelte#L4). | Make observed voting power primary and counts secondary; distinguish block, nil, and not-observed power. Quorum status must refer to power voting for the same BlockID. |
| P1 | WebSocket subscriptions read and mutate one map concurrently. Lossy queues have no same-connection recovery path. | [subscription read](https://github.com/InjectiveLabs/cmt-top/blob/2ae3404/internal/web/ws.go#L240), [subscription mutation](https://github.com/InjectiveLabs/cmt-top/blob/2ae3404/internal/web/ws.go#L320), [client sequence handling](https://github.com/InjectiveLabs/cmt-top/blob/2ae3404/web/src/lib/ws.ts#L38). | Give subscription state one owner or synchronization. Specify stream ordering and resnapshot recovery after loss; account for intentionally filtered events in sequence semantics. |
| P1 | Documented Docker quick start sets the application bind to container loopback, making the published host port ineffective. | [.env.example](https://github.com/InjectiveLabs/cmt-top/blob/2ae3404/.env.example#L32), [Compose env import](https://github.com/InjectiveLabs/cmt-top/blob/2ae3404/docker-compose.yml#L26). | Explicitly bind inside the container to `0.0.0.0`, retaining the existing localhost host-port binding. Test the documented copy-env/compose procedure from a clean checkout. |
| P1 | HTTP RPC requests lack an effective per-attempt deadline; a hung request can stall its poller before failover. Required listener failures are logged without terminating web mode. | [RPC client](https://github.com/InjectiveLabs/cmt-top/blob/2ae3404/internal/chain/cometrpc/client.go#L59), [poller call](https://github.com/InjectiveLabs/cmt-top/blob/2ae3404/internal/core/orchestrator.go#L199), [web startup](https://github.com/InjectiveLabs/cmt-top/blob/2ae3404/cmd/cmt-top/main.go#L199). | Add bounded request and poll budgets, propagate required startup failures, and test an occupied port and a server that never responds. |
| P1 | TUI sort and search-completion callbacks synchronously queue work onto the same event loop, causing a deadlock. Search also inherits global quit/sort/pause shortcuts. | [TUI callbacks](https://github.com/InjectiveLabs/cmt-top/blob/2ae3404/internal/tui/tui.go#L89), [queued render](https://github.com/InjectiveLabs/cmt-top/blob/2ae3404/internal/tui/tui.go#L161). | Separate direct event-loop rendering from queued background rendering; route keys by interaction mode and make Escape cancel search. |
| P2 | The AppHash claim exceeds implementation: `monitored_rpcs` has no runtime consumer, while the visible panel groups votes by BlockID hash. | [configuration](https://github.com/InjectiveLabs/cmt-top/blob/2ae3404/internal/config/config.go#L31), [panel](https://github.com/InjectiveLabs/cmt-top/blob/2ae3404/web/src/components/DivergencePanel.svelte#L21). | Correct copy immediately. Add actual comparison across endpoints only with height alignment, chain identity checks, lag handling, and explicit comparison evidence. |
| P2 | Fixed 280/380 px side panels, small muted type, compressed controls, and no breakpoints obstruct scanning and mobile use. | [layout and styles](https://github.com/InjectiveLabs/cmt-top/blob/2ae3404/web/src/app.css#L31), [table controls](https://github.com/InjectiveLabs/cmt-top/blob/2ae3404/web/src/components/ValidatorTable.svelte#L39). | Give validators primary width, use progressive disclosure, responsive stacking, readable contrast and keyboard-accessible controls. |
| P2 | Divergence history ordering is inconsistent, and existing member/hash data cannot be investigated from the UI. | [history rendering](https://github.com/InjectiveLabs/cmt-top/blob/2ae3404/web/src/components/DivergencePanel.svelte#L51), [history reducer](https://github.com/InjectiveLabs/cmt-top/blob/2ae3404/web/src/lib/stores.ts#L176). | Normalize and deduplicate incidents, order newest first, and expand groups to show members and full hashes. |
| P2 | Release workflow has no test gate; clean Go checks require generated assets; build/lint scripts can hide failures. | [release workflow](https://github.com/InjectiveLabs/cmt-top/blob/2ae3404/.github/workflows/release.yml#L77), [embed](https://github.com/InjectiveLabs/cmt-top/blob/2ae3404/internal/web/embed.go#L9), [Makefile](https://github.com/InjectiveLabs/cmt-top/blob/2ae3404/Makefile#L27), [frontend scripts](https://github.com/InjectiveLabs/cmt-top/blob/2ae3404/web/package.json#L6). | Add PR and release checks, a reliable clean-checkout build or tracked stub, deterministic installs, frontend type checks, and meaningful integration fixtures. |

Consensus terminology matters: the protocol's commit threshold concerns more than two-thirds of total voting power for a block. BlockID hashes identify blocks; AppHash represents application state. Observed vote splits alone do not prove application nondeterminism. The UI should preserve that distinction. [CometBFT data structures](https://docs.cosmos.network/cometbft/latest/spec/core/Data_structures).

## UI design plan

### Layout and visual hierarchy

Keep the compact monitoring character, with a clear order: identify the chain, judge freshness, understand consensus, then investigate a validator or incident.

1. **Header:** `cmt-top`, chain identity, browser feed, upstream mode/health, and data age. Use words such as `Streaming`, `Polling`, or `Stale`; `open` is an implementation state. Avoid suggesting that the public RPC's validator is owned by the dashboard user: use `Connected node validator`, and treat a user watchlist as a separate feature.
2. **Consensus summary:** committed height and active height/round, named step, average block time with its sampling window, and weighted prevote/precommit bars. Show counts as secondary context. A same-BlockID quorum reference is useful only with the corresponding group data and correct threshold semantics.
3. **Main workspace:** the validator table receives most of the width. A compact incidents area expands when there is an incident or a selected validator. Move static node metadata into a disclosure section instead of a permanent wide left column.
4. **Responsive behavior:** wide screens use a main table plus optional details; tablets stack the summary above the table; phones stack consensus, validator list, and incidents. Hide optional columns only when full values remain available in detail. Avoid page-level horizontal overflow.

Use a neutral high-contrast surface, one restrained accent, and green/amber/red only with explicit status text. Use readable UI text for labels and monospaced tabular numerals/hashes for data. Replace raw numeric steps and unexplained `pv`/`pc` glyphs with named states or a persistent compact legend. Avoid decorative charts that do not support an operator decision.

### Validator workflow

- Sticky, keyboard-operable column headers with visible direction and `aria-sort`.
- Labeled search for moniker, consensus address, and operator address; clear-search action and explicit no-results state.
- Filters for not-observed votes, nil votes, proposer, and a watched validator. Do not describe a vote missing from one node's live feed as a confirmed missed block or downtime.
- Detail panel with full addresses, copy actions, voting power, current vote BlockIDs, and proposer context. Add configured explorer links in package E once the backend exposes their configuration.
- Stable sorting and selection as new events arrive. Batch rendering where profiling shows excessive updates; do not add virtualization without measured need.
- Optional pause of the visible view for investigation, clearly labeled with a frozen timestamp. Resume by reconciling with a fresh snapshot; upstream collection continues.

### Incident workflow

- Separate **Vote splits** from **RPC AppHash comparison**. The latter starts as `Not configured` or `Unavailable`, never green by default.
- Show height, round, vote type, observed power, leading group, other groups, nil/unobserved power, and lifecycle status.
- A leading uncommitted group is `Leading`; use `Canonical` only when commit evidence establishes it.
- Expand a group to inspect its full hash and validators. Keep group identity consistent between chart and member list.
- Show incidents newest first, deduplicate snapshot/incremental updates, and provide a compact resolved history.
- Present `No split observed in the received votes` separately from `Waiting for data` and `Feed unavailable`. None of these alone establishes global chain health.

### Loading, failure, and accessibility

Provide distinct initial loading, invalid/missing token, upstream unavailable, polling fallback, stale data, reconnecting, empty validator set, and no-search-results states. Keep last-known values visible when useful, with a timestamp and stale treatment. Put concise recovery guidance near the failure and raw diagnostic errors behind a disclosure.

Use semantic headings and table markup, visible search labels, native buttons for sort/actions, visible focus, text alternatives for votes/bars, and restrained status announcements. Test keyboard navigation and narrow screens; avoid announcing every live vote to assistive technology. Respect reduced motion and make controls comfortable to use on touch screens.

### TUI parity

Fix sort/search deadlocks and shortcut interception before adding new TUI features. Preserve selection and scroll on redraw, display pause immediately, support Unicode display widths, and mirror upstream health and weighted consensus semantics from the web UI.

## Implementation sequence

These are bounded work packages, not calendar commitments. Start with A and B; C can proceed once its data contract is agreed. Keep each package reviewable and pair it with the scenarios that prove it works.

| Package | Scope | Dependencies | Completion evidence |
| --- | --- | --- | --- |
| A — Reliable startup and checks | Docker bind, required listener errors, clean-checkout assets, deterministic installs, truthful lint exit codes, PR CI and release gates. | None. | Documented fresh startup serves UI from the host; occupied port exits nonzero; clean CI builds/type-checks/tests successfully. |
| B — Correct monitoring state | Unified height/round transitions, address-keyed refresh, pinned validator pages, nil-group detection, one incident-created event, subscription synchronization, RPC deadlines. | None; run alongside A. | Deterministic replay fixtures cover late/missing/reordered events, unequal power, validator reorder/replacement, nil majority, concurrent subscriptions, and hung RPC failover. |
| C — Complete live web contract | Typed payloads, every supported event handled, explicit millisecond units, null/removal updates, upstream freshness, HTTP fallback publication, snapshot recovery after gaps/reconnect. | B's state model. | Browser state converges to authoritative snapshot after each fixture; same-connection drops recover; unavailable upstream cannot appear healthy; time units match first paint and updates. |
| D — Responsive operator UI | New header/summary hierarchy, table-first layout, weighted bars, search/filter/sort states, details, incident history, keyboard/touch support. | C's health/event contract. | Desktop/tablet/mobile and keyboard checks pass for healthy/loading/stale/error/empty/divergent states; no page overflow at 390 px; critical information remains reachable at 320 px. |
| E — Complete promised capabilities | Real same-height RPC AppHash comparison, bounded block history, explorer links, remaining config/documentation cleanup, bounded observability. | B/C; AppHash logic is independent of visual polish. | Lag is distinct from mismatch; incompatible chains are rejected; matching-height mismatches show evidence; `/api/blocks` supplies real bounded history; metrics do not grow one series per height indefinitely. |

TUI repairs belong in B and can run independently of the web layout work. Correct misleading AppHash copy in the first patch; do not wait for package E. Full cross-RPC comparison should ship only after its data and failure cases are tested.

## Verification scenarios for the implementation

| Scenario | Required result |
| --- | --- |
| Validators have 70/20/10 power; only the first votes | Show 70% observed power and 1/3 validators. Avoid claiming a commit unless the relevant same-BlockID precommit condition is met. |
| 70% nil votes and 30% for one BlockID | Show nil participation without inventing a two-block split. |
| A vote arrives after its height committed | Preserve historical evidence if needed; never repaint the active validator row. |
| Missing NewRound followed by higher-height votes | Advance the active context correctly; clear previous round votes and stale round numbers together. |
| Same-count validator set is reordered/replaced | Votes remain attached only to their original consensus addresses in the appropriate context. |
| Upstream WS fails but browser WS remains connected | Show upstream failure or polling fallback; retain last-good timestamps; clear the error after recovery. |
| Server drops events or restarts | Detect desynchronization under documented sequence semantics, take a consistent snapshot, and resume with no duplicate history. |
| Upgrade disappears or validators change | Remove stale upgrade data and refresh the table without a page reload. |
| Authentication fails | Show a useful authentication state; do not silently spin through reconnect attempts. |
| TUI search contains q, p, and s; Escape cancels | Remain in search mode without quitting, sorting, pausing, or deadlocking. |
| One monitored endpoint is behind | Show lag/insufficient comparison data, not an AppHash mismatch. |
| A long-running session produces many incidents | Bounded memory/history and bounded metric series; count unique incidents correctly. |

## Additional contract cleanup

- `/api/blocks` currently always returns an empty array; avoid adding a trend chart until a real series is exposed. [handler](https://github.com/InjectiveLabs/cmt-top/blob/2ae3404/internal/web/server.go#L327).
- `headless` starts the web SPA/API although documentation calls it metrics-only; `ui.web.disabled` is not consumed. Define and test one explicit behavior. [mode switch](https://github.com/InjectiveLabs/cmt-top/blob/2ae3404/cmd/cmt-top/main.go#L206).
- Readiness currently latches after the first positive height. Define startup readiness separately from freshness, publish last successful receipt times, and avoid restarting healthy processes merely because an upstream service is down. [readiness](https://github.com/InjectiveLabs/cmt-top/blob/2ae3404/cmd/cmt-top/main.go#L168).
- Height/round-labelled divergence metrics accumulate, and drop counts are not exposed. Use bounded labels and export freshness/drop measurements that support diagnosis. [metrics](https://github.com/InjectiveLabs/cmt-top/blob/2ae3404/internal/metrics/metrics.go#L103).
- Explicit missing config paths should fail clearly; the sample `monitored_rpcs` comment is under the wrong TOML table. Implement or remove inert configuration such as debounce/simulation and unused explorer settings.
- Replace token-bearing page URLs with a deliberate authentication flow appropriate to deployment, and remove the token from browser history after use. Retain existing bearer and same-origin protections while designing this change.
- The README references an absent dashboard screenshot, and the `e2e` target currently runs existing unit tests rather than a distinct end-to-end suite. Update examples and claims to match verified behavior.

## First implementation recommendation

Start with a small reliability milestone: correct deployment startup, truthful health/freshness, weighted consensus display, stale-event protection, validator identity preservation, and regression fixtures. Then deliver the responsive shell and table/incident details against that corrected data model. Keep the current compact stack and add only capabilities that improve an operator's ability to trust or investigate the data.
