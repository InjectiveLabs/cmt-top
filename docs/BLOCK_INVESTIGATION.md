# Investigating a stalled upgrade block

For a step-by-step demo with screenshots, start with the
[Investigate block walkthrough](investigate-block/README.md).

Run cmt-top before the expected upgrade so it can retain the votes it receives. In the web dashboard, choose **Investigate block** or open `/blocks/<height>/rounds`. A chosen height stays pinned when consensus advances. The page follows the newest observed round until you select a particular round; use a reference round to compare validator behavior over time.

## Read the evidence

1. Check upstream freshness and observation coverage. A responsive dashboard or an HTTP status response does not imply that all votes are arriving. Rounds before monitoring started, during a disconnect, or outside retention cannot be reconstructed by this page.
2. Compare prevote and precommit groups **within the same height and round**. Hash labels remain consistent across both phases. Inspect the full hash and validator members rather than relying on shortened prefixes or colors.
3. Inspect nil votes separately from votes not observed. An explicit nil vote is a received observation. Not observed describes this RPC feed and does not establish validator downtime.
4. Compare a reference round to find validator cohorts that repeatedly diverge, or whose observed vote changes between phases. This identifies operators and logs worth comparing; it does not assign a cause.
5. Preserve the JSON evidence before restarting cmt-top or letting the retention window expire. The export is a record of received RPC observations, not verified signed consensus evidence.

CometBFT can require further rounds because a proposal or votes arrived too late, a proposer was unavailable, or a proposal was invalid. Changes between rounds and nil votes are not by themselves proof of faulty execution. See the [CometBFT consensus specification](https://github.com/cometbft/cometbft/blob/v0.38.21/spec/consensus/consensus.md).

## Confirm a suspected binary or execution difference

For validators in different observed cohorts, compare the **application** binary version, source commit, binary checksum, upgrade plan/height, enabled build options, and relevant configuration with the operators. The connected RPC's CometBFT version is neither an inventory of validator binaries nor a substitute for their application build information.

Compare proposal acceptance/rejection and upgrade execution logs at the exact height and round. Reproducing a suspected execution difference requires matching prior application state, proposal input, binary, and configuration. Deterministic proposal processing and state transitions are application responsibilities, described in the [ABCI application requirements](https://github.com/cometbft/cometbft/blob/v0.38.21/spec/abci/abci%2B%2B_app_requirements.md).

Use the separate RPC AppHash comparison as additional evidence only at the same available header height on the same chain. While a block remains uncommitted, a later header reflecting its execution may not exist. A matching earlier header does not prove that the stalled upgrade will execute identically. A BlockID vote hash is not an AppHash.

Two hashes reported for the same validator, height, round, and vote phase are shown as **conflicting observations**. The monitor does not verify vote signatures or produce a cryptographic double-sign proof. Confirm against signed evidence and independent logs before drawing that conclusion. Because conflicting observations can put one validator in multiple hash groups, those group powers can overlap; unique observed voting power is counted separately.

## Retention and API

The archive records both prevotes and precommits, including rounds without a split and rounds with only a round event. It retains at most `divergence.history_size` heights and 128 observed rounds per height, with at most four distinct hashes per validator and phase and 64 unknown voter addresses per round. Retention is memory-only and resets when the server restarts. The response reports coverage gaps, eviction and truncation; missing observations are never invented.

Each round keeps the validator metadata available to the monitor when it was first observed, or when the initial validator set finishes loading. This is an observed roster, not an independently fetched historical validator set for every round. Votes from addresses outside that roster have unknown power and make the roster incomplete.

`GET /api/blocks/{height}/rounds` uses the dashboard's existing bearer-token protection. Heights must be canonical positive decimal integers within JavaScript's exact integer range. A valid height without retained observations returns HTTP 200 with `found: false` and its availability status; malformed heights return HTTP 400. The response combines the selected height's archive with a separate `context` containing the current chain height, health and upgrade plan. This keeps historical observations distinct from current chain state.

For a deterministic local example after `make web`:

```sh
go run -tags webui ./internal/web/testdata/dashboard -scenario upgrade -listen 127.0.0.1:18082
```

The fixture is synthetic; it does not contact a blockchain.
