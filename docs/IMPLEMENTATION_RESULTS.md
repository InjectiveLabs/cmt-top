# Implementation results

Implemented 16 September 2026, following [the app review and plan](APP_REVIEW_AND_PLAN.md). This records the checks performed during that implementation, before the repository migration. It is not a release or deployment status page.

## Monitoring correctness

- Active consensus height is separate from committed height. Shared transition rules reject old heights, rounds, and conflicting duplicate observations; a missing round event no longer leaves votes attached to an old context.
- Validator refreshes merge by consensus address. Pagination is pinned to one height and rejects incomplete or inconsistent results.
- Nil-majority rounds no longer create false block splits. Incident creation is counted once; updates and resolution preserve the existing incident.
- RPC requests have bounded deadlines and can fail over. Health reports browser-independent upstream mode and successful data timestamps. HTTP polling can recover progress while the stream is unavailable or stale, with a guard against late responses clearing a recovered stream.
- RPC AppHash comparison checks the same block-header height and chain on each endpoint. Lag, incompatible chains, and request failures remain distinct from a mismatch. Vote splits remain a separate signal.
- Recent blocks are retained in a bounded 120-sample history. Metrics avoid height-dependent series growth and expose dropped messages and freshness.

## Web and terminal interface

- Replaced the fixed three-column layout with a responsive summary, primary validator table, and incident panels.
- Consensus bars use exact integer voting power and distinguish leading BlockID, other blocks, nil votes, and votes not observed. The two-thirds reference applies to one BlockID.
- Added accessible sorting, search and empty states, vote/proposer/watchlist filters, full validator details, copy buttons, and configured explorer links.
- Vote-split groups disclose full hashes and member validators. RPC comparisons disclose endpoint evidence. The block chart uses recorded intervals.
- Pause freezes the investigated view while collection continues; resume reconciles a fresh snapshot.
- Token authentication has an explicit form and error state. Token-bearing page URLs are removed from the address bar; session storage is scoped to the browser tab.
- WebSocket sequences are per client after subscription filtering. Gaps trigger an authoritative resnapshot; periodic snapshots reconcile metadata, error clearing, and upgrade removal.
- TUI sort/search callbacks remain responsive, search accepts shortcut characters, pause updates immediately, selection survives refresh, and truncation respects Unicode display widths.

## Build and operations

- Required listener failures terminate with a nonzero exit status. Headless mode serves metrics without starting the web server.
- Compose overrides container listener addresses to all interfaces while retaining loopback-only host publication.
- Clean Go checkouts use an informative web stub. `make build` and Docker explicitly embed the built SPA with the `webui` tag.
- Added PR checks and a release gate for race tests, static checks, frontend validation, bundled builds, and a container smoke test. Dependency installation uses the frozen lockfile; lint failures are no longer masked.
- Configuration rejects unknown keys, explicit missing files, invalid intervals, and invalid thresholds. Previously inert debounce, simulation, and Mintscan-path fields were removed; `chain.name` is an optional display label.

## Verification

| Check | Result |
| --- | --- |
| Default Go build | Full `go test -race ./...` and `go vet ./...` passed. |
| Embedded web build | Full `go test -race -tags webui ./...` and tagged binary build passed. |
| Frontend | Svelte type/accessibility check reported zero errors and warnings; all 43 reducer/model/transport/round-investigation tests and the production build passed. |
| Deterministic regressions | Covered stale/reordered events, validator identity changes, nil-majority splits, hung RPC failover, comparator lag/chain/error cases, API and WebSocket authentication, subscription races, sequence loss/recovery, periodic reconciliation, listener failure, and simulated TUI input. |
| Browser interaction | Verified search/no results/reset, power sorting and `aria-sort`, validator details, copy, watchlist filtering, incident evidence, authentication rejection/acceptance, and token URL removal. |
| Responsive layout | Inspected desktop and 390/320-pixel widths, including long monikers and expanded hashes; no page-level horizontal overflow. |
| Live chain | Default Injective RPC loaded 45 validators with advancing active/committed heights. Pause remained frozen while 66 more blocks arrived; resume caught up. |
| Container runtime | Built the Docker image and started a temporary Compose project using `.env.example`. Container became healthy; host requests to health, readiness, UI, state, block history, and metrics succeeded. History reached its 120-sample bound. |

The browser outage and split scenarios used an explicit synthetic fixture at `internal/web/testdata/dashboard`; their incidents are not claims about the live chain. The automated suites use local fixtures and do not require public RPC availability. CI configuration was reviewed and its principal commands run locally; a hosted GitHub Actions run has not been triggered. Sustained load, exhaustive chain compatibility, and external security auditing remain outside this implementation's verification.

To run the fixture after building the frontend:

```sh
go run -tags webui ./internal/web/testdata/dashboard -listen 127.0.0.1:18082 -scenario split
```

Other fixture scenarios are `healthy` and `unavailable`; `-token` enables the authentication test flow.

## Block investigation follow-up

Added `/blocks/{height}/rounds` for investigating a stalled upgrade height, backed by the authenticated `/api/blocks/{height}/rounds` archive endpoint. It keeps both phases, late and conflicting observations, recorded rosters, exact voting-power strings, coverage gaps and commit evidence within explicit memory bounds. The page provides a round timeline, phase hash groups, per-validator/reference-round comparison, filters, pause/resume and JSON export. See the [operator guide](BLOCK_INVESTIGATION.md) for interpretation and limitations.

The separate backend, UI and testing agents completed archive/core/API regressions, 17 new frontend tests, and independent review. Full Go race tests, vet, tagged Go integration/build, and frontend checks/tests/build passed. Browser checks covered 390/320-pixel layouts, round and reference selection, cohort/conflict filters, full-hash copying, invalid/future heights, back navigation, pause/resume, authenticated deep links and session reload. A live Injective investigation stayed pinned after its block committed while the chain advanced. The exported synthetic evidence was read back and verified to contain all four rounds with exact power strings. Use `-scenario upgrade` with the fixture command above to inspect this scenario at `/blocks/1001/rounds`.
