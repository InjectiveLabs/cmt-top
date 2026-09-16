# Using cmt-top

Configuration, operator recipes, and API details. For a quick start, see the [README](../README.md).

## Configuration

Three layers, lowest precedence first: **defaults < TOML config < env vars < CLI flags**.

### TOML

Default search path: `$XDG_CONFIG_HOME/cmt-top/config.toml`, falling back to
`~/.config/cmt-top/config.toml`. Or pass `--config /path/to/file.toml`.

A documented sample lives at [config.example.toml](../config.example.toml). Generate
one matching the running defaults:

```sh
mkdir -p ~/.config/cmt-top
./bin/cmt-top --print-config > ~/.config/cmt-top/config.toml
```

### Environment variables

See [.env.example](../.env.example). Common knobs:

| Variable | Purpose |
|---|---|
| `CMTOP_RPC` | Primary CometBFT RPC URL (HTTP+WS). |
| `CMTOP_LCD` | Cosmos REST API for monikers + upgrade plan. |
| `CMTOP_MONITORED_RPCS` | CSV of extra RPCs for cross-endpoint AppHash compare. |
| `CMTOP_MODE` | `tui` \| `web` \| `both` \| `headless` |
| `CMTOP_WEB_LISTEN` | Web bind, default `127.0.0.1:8080`. |
| `CMTOP_WEB_TOKEN` | Bearer token. Recommended when binding non-loopback (a warning is logged if unset). |
| `CMTOP_LOG_LEVEL` | `debug` \| `info` \| `warn` \| `error` |
| `CMTOP_BECH32_PREFIX` | Operator address prefix (default `inj`). |

### CLI flags

```
--config <path>             override config file
--print-config              print resolved config and exit
--rpc <url>                 RPC URL; pass --rpc multiple times for failover
--lcd <url>                 LCD/REST URL
--monitored-rpc <url>       extra RPC for AppHash compare; pass multiple times
--mode tui|web|both|headless
--web-listen <addr>         default 127.0.0.1:8080
--web-token <token>         recommended for non-loopback bind
--metrics-listen <addr>     default 127.0.0.1:9091
--bech32-prefix <prefix>    default inj
--divergence-threshold <pct>  default 5.0
--log-level <level>
```

A positional argument is accepted as shorthand for the primary RPC:

```sh
./bin/cmt-top https://my-node:26657
```

---

## Targeting other chains

Point cmt-top at a CometBFT RPC and, optionally, a Cosmos LCD/REST endpoint
for moniker enrichment. Set the chain's bech32 prefix and explorer URL in config.

```sh
./bin/cmt-top \
  --rpc http://your-node:26657 \
  --lcd http://your-node:1317 \
  --bech32-prefix cosmos
```

For frequent use, drop these into a TOML config so a bare `./cmt-top`
launches against your chain of choice. See [config.example.toml](../config.example.toml).

What works generically:
- Validator set, voting power, monikers (via LCD `cosmos.staking.v1beta1`)
- Vote aggregation per round, prevote/precommit per validator
- BlockID vote-split detection and separate same-height RPC AppHash comparisons
- Upgrade plan readout (via LCD `cosmos.upgrade.v1beta1`)
- Block-time average, Prometheus metrics

What is Injective-specific:
- The default endpoints baked into [config.go](../internal/config/config.go) — replace via `--rpc`/`--lcd`/`CMTOP_RPC`/`CMTOP_LCD`
- The default explorer URL template — replace via `chain.explorer_url` in TOML

Nothing else assumes Injective.

## Common recipes

### Run against a private sentry, expose dashboard on the LAN

```sh
TOKEN=$(openssl rand -hex 32)
./bin/cmt-top \
  --rpc http://10.0.0.5:26657 \
  --lcd http://10.0.0.5:1317 \
  --mode web \
  --web-listen 0.0.0.0:8080 \
  --web-token "$TOKEN"

# Open from another machine:
#   http://10.0.0.5:8080/?token=<token>
```

The dashboard also accepts the token through its sign-in form. A legacy `?token=` link is removed from the address bar after the token is read; the token is kept for the current browser tab.

A non-loopback bind with an empty token still starts, but logs a warning that the dashboard is exposed without auth.

### Compare AppHash across multiple RPCs

```sh
./bin/cmt-top \
  --rpc http://primary:26657 \
  --monitored-rpc http://node-a:26657 \
  --monitored-rpc http://node-b:26657
```

The RPC AppHash panel compares headers at the reference endpoint’s latest known
committed height, at the status refresh interval. Each endpoint is checked for
chain identity and lag before comparison. A mismatching hash at the same height
is shown separately from vote splits, connection errors, and incomplete comparisons.
BlockID vote splits alone do not establish application nondeterminism.

### Investigate a block stalled during an upgrade

Follow the [Investigate block screenshot walkthrough](investigate-block/README.md)
for a runnable demo and examples of round selection, hash splits, validator comparison,
and evidence export.

Open **Investigate block** in the web dashboard, or visit
`/blocks/<height>/rounds`. The page stays pinned to that height while collecting
new rounds. Compare the prevote and precommit groups, select an earlier reference
round, and inspect which validators stay together or change their votes. Export
the observations before restarting the monitor or their retention window expires.

The page captures both vote phases regardless of `divergence.include_prevotes`.
History is local to this running process; it cannot reconstruct votes from before
monitoring began. It retains the configured `divergence.history_size` heights and
the latest 128 observed rounds per height, with explicit coverage and truncation
indicators. Late observations can fill retained rounds without changing the live
dashboard's active round.

Use the identified validator cohorts to compare operators' application build
versions/checksums, upgrade configuration, and execution logs. Vote hashes do not
reveal the binary running on each validator. See the [block investigation guide](BLOCK_INVESTIGATION.md)
for interpreting nil votes, conflicts, and same-height AppHash evidence.

### Headless (metrics only, no UI)

```sh
./bin/cmt-top --mode headless --metrics-listen 0.0.0.0:9091
curl -s http://127.0.0.1:9091/metrics | grep cmt_top_
```

### TUI + web at once

```sh
./bin/cmt-top --mode both
```

---

## Docker

Release images use `public.ecr.aws/l9h3g6c6/cmt-top:<version>`; `latest` follows
stable releases. Images target `linux/amd64`. Publishing requires the repository
CI AWS role and ECR repository to be configured.

The image builds the SPA and Go binary in separate stages. The Alpine runtime
includes certificates, timezone data, and curl for its health check. It runs as
a non-root user and starts in web mode on `0.0.0.0:8080`.

### Build

```sh
docker build -t public.ecr.aws/l9h3g6c6/cmt-top:latest --build-arg VERSION=$(git describe --tags --always) .
```

This local tag matches Compose's default image, so the commands below also work
before the first release has published an image to the new ECR repository.

### Run with `docker compose`

```sh
cp .env.example .env             # edit endpoints / token if exposing externally
docker compose up -d
docker compose logs -f cmt-top
```

Compose binds the application to `0.0.0.0` inside the container, overriding the
local-development bind in `.env`. It binds host ports to `127.0.0.1` by default — drop the prefix in
[docker-compose.yml](../docker-compose.yml) only when `CMTOP_WEB_TOKEN` is set.

To use a TOML config inside the container, uncomment the `volumes:` and
`command:` lines in [docker-compose.yml](../docker-compose.yml) and mount your
file at `/app/config.toml`.

### Run without compose

```sh
docker run --rm -p 127.0.0.1:8080:8080 -p 127.0.0.1:9091:9091 \
  --env-file .env \
  cmt-top:local
```

### Prometheus + Grafana

```sh
docker compose --profile metrics up -d
# Prometheus → http://localhost:9090
# Grafana    → http://localhost:3000  (anonymous viewer; admin/admin)
```

Scrape config lives at [deploy/prometheus.yml](../deploy/prometheus.yml).

---

## Endpoints

| Path | Description |
|---|---|
| `GET /` | Svelte SPA (cold-load). |
| `GET /api/state` | Complete snapshot: active/committed heights, validators, vote splits, health, blocks, and RPC comparisons. |
| `GET /api/validators?search=<q>` | Server-side filter. |
| `GET /api/validators/{addr}` | Single validator drilldown. |
| `GET /api/divergence` | Live divergence rounds. |
| `GET /api/divergence/history` | Recent resolved rounds. |
| `GET /api/blocks` | Up to 120 retained committed block samples (memory only). |
| `GET /blocks/{height}/rounds` | Dedicated page pinned to one block's observed rounds. |
| `GET /api/blocks/{height}/rounds` | Authenticated round archive, validator phase votes, groups, coverage, retention, and current chain context. |
| `GET /api/chain` | Chain card + upgrade plan. |
| `WS  /ws?token=<t>` | Streaming envelope (snapshot + patches). |
| `GET /healthz` | Process liveness. |
| `GET /readyz` | Node status and validator set initialized; upstream freshness is reported separately. |
| `GET /metrics` | Prometheus (separate listener, default `127.0.0.1:9091`). |

---

## Development

```sh
make build         # web bundle + Go binary
make build-no-web  # binary only (web returns 503 from a stub)
make web           # Svelte SPA into internal/web/dist
make test          # Go unit tests
make test-race     # with -race
make e2e           # deterministic local RPC/WS integration tests
make lint          # golangci-lint (nonzero exit on missing tool or findings)
```


The Go-only build intentionally serves a 503 web stub. `make build` and Docker
build the frontend and compile Go with `-tags webui`. For a manual web build:

```sh
cd web && pnpm install --frozen-lockfile && pnpm check && pnpm test && pnpm build
cd .. && go run -tags webui ./cmd/cmt-top --mode web
```

Clean-checkout Go tests do not require generated frontend assets. Blacksmith CI and
release gates run the Go race detector, vet, frontend checks/tests/build, and a
container smoke test using the documented Compose environment.

### CI and image publishing

Checks and release builds run on Blacksmith. Stable version tags publish the
version image and update `latest`; prereleases publish only the version image.
Manual release runs must target a version tag. See [CI and release setup](CI.md)
for the Blacksmith and AWS configuration.

### Live data and recovery

The web header distinguishes the browser feed from upstream RPC health. `Streaming`
means fresh WebSocket data; `Polling` means the HTTP fallback is supplying data;
`Stale` preserves older values with timestamps; `Unavailable` means no usable data
has arrived or both sources have failed. A vote that is not observed in this
feed is not evidence of validator downtime. Consensus bars use voting power,
with validator counts as secondary context.

WebSocket sequences are scoped to each client connection, starting at 1. Channel
filtering does not introduce sequence gaps. Clients request `{ "type": "resync" }`
after a gap and ignore patches until the next `state.snapshot`; reconnecting
also starts with a snapshot. The server sends a complete snapshot at least every
second while connected, including cleared errors and removed upgrade plans.
There is no historical `since` replay. Pausing freezes the visible snapshot;
resuming requests fresh state.

`headless` serves metrics only. `ui.web.disabled = true` disables web in `both`
mode; combining it with `web` mode is an explicit configuration error. Missing
explicit config files and unknown TOML keys are errors. The previously unused
`divergence.debounce`, `divergence.simulate_divergence`, and `chain.mintscan_path`
settings have been removed; remove them from older config files. `chain.name`
is an optional display label, while network identity always comes from the RPC.

---

## Architecture

```
CometBFT (WS + HTTP) → chain client → orchestrator (sole state writer)
                                          │
                                          ▼
                                     events.Bus
                                  ┌───────┼───────┐
                                  ▼       ▼       ▼
                                 TUI    web UI   Prometheus
                                          │
                                divergence tracker
```

The orchestrator owns state changes; its pollers and event handler synchronize
through `state.State`. The bus is
lossy: slow subscribers (a stuck WS client, a paused TUI) drop events with a
metric counter, never block the publisher.
