# cmt-top

A `top`-style live dashboard for any CometBFT-based Cosmos chain (Cosmos Hub,
Osmosis, Celestia, Injective, dYdX, Neutron, …). Point it at an RPC, watch
consensus happen.

The defaults ship Injective endpoints baked in, so `./cmt-top` with no
arguments connects to a public Injective RPC — but every endpoint, prefix, and
chain-specific knob is overridable via flag, env, or TOML config. See
[Targeting other chains](#targeting-other-chains) below.

Inspired by [tmtop](https://github.com/QuokkaStake/tmtop) (which itself was
inspired by [pvtop](https://github.com/blockpane/pvtop)) and built independently
from scratch. The shared niche — a terminal dashboard for CometBFT consensus —
is the only overlap; the implementation, transport (WS-first), data model
(BlockID-aware votes), web UI, and divergence detection are all original.

![dashboard](docs/dashboard.png)

## Features

- **TUI** and a **Svelte web UI** sharing one event bus
- **WebSocket subscriptions** to CometBFT events (`NewBlock`, `NewRound`, `Vote`,
  `ValidatorSetUpdates`); HTTP polling only as fallback
- **Live AppHash divergence panel** — group validators by the `BlockID` they voted
  for, weighted by voting power; surface nondeterminism in real time
- **Sort / filter / search** validators, with proposer highlighted each round
- **Prometheus `/metrics`** for alerting
- **TOML config** with `CMTOP_*` env vars and CLI flags; defaults bake-in Injective
- Multi-RPC failover with circuit breaker

---

## Quick start

### From source

```sh
make build                       # builds web bundle + Go binary into bin/
./bin/cmt-top               # connects to public Injective RPC, TUI mode
./bin/cmt-top --mode web    # web UI on http://127.0.0.1:8080
```

### With Docker

Pre-built images are published to Docker Hub on every GitHub Release:

```sh
docker pull 0xrigo/cmt-top:latest
```

Or build locally via compose:

```sh
cp .env.example .env              # tune endpoints / mode
docker compose up -d
open http://localhost:8080
```

Add `--profile metrics` to also bring up Prometheus (`:9090`) and Grafana (`:3000`):

```sh
docker compose --profile metrics up -d
```

---

## Configuration

Three layers, lowest precedence first: **defaults < TOML config < env vars < CLI flags**.

### TOML

Default search path: `$XDG_CONFIG_HOME/cmt-top/config.toml`, falling back to
`~/.config/cmt-top/config.toml`. Or pass `--config /path/to/file.toml`.

A documented sample lives at [config.example.toml](config.example.toml). Generate
one matching the running defaults:

```sh
./bin/cmt-top --print-config > ~/.config/cmt-top/config.toml
```

### Environment variables

See [.env.example](.env.example). Common knobs:

| Variable | Purpose |
|---|---|
| `CMTOP_RPC` | Primary CometBFT RPC URL (HTTP+WS). |
| `CMTOP_LCD` | Cosmos REST API for monikers + upgrade plan. |
| `CMTOP_MONITORED_RPCS` | CSV of extra RPCs for cross-endpoint AppHash compare. |
| `CMTOP_MODE` | `tui` \| `web` \| `both` \| `headless` |
| `CMTOP_WEB_LISTEN` | Web bind, default `127.0.0.1:8080`. |
| `CMTOP_WEB_TOKEN` | Bearer token. **Required** when binding non-loopback. |
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
--web-token <token>         required for non-loopback bind
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

Anything CometBFT-based works. You just need a public RPC and (optionally) an
LCD/REST endpoint for moniker enrichment, plus the chain's bech32 prefix.

```sh
# Cosmos Hub
./bin/cmt-top \
  --rpc https://rpc.cosmos.network:443 \
  --lcd https://rest.cosmos.network \
  --bech32-prefix cosmos

# Osmosis
./bin/cmt-top \
  --rpc https://rpc.osmosis.zone:443 \
  --lcd https://lcd.osmosis.zone \
  --bech32-prefix osmo

# Celestia
./bin/cmt-top \
  --rpc https://public-celestia-rpc.numia.xyz \
  --lcd https://public-celestia-lcd.numia.xyz \
  --bech32-prefix celestia

# dYdX
./bin/cmt-top \
  --rpc https://dydx-mainnet-full-rpc.public.blastapi.io \
  --bech32-prefix dydx

# Neutron
./bin/cmt-top \
  --rpc https://rpc-kralum.neutron-1.neutron.org \
  --bech32-prefix neutron
```

For frequent use, drop these into a TOML config so a bare `./cmt-top`
launches against your chain of choice. See [config.example.toml](config.example.toml).

What works generically:
- Validator set, voting power, monikers (via LCD `cosmos.staking.v1beta1`)
- Vote aggregation per round, prevote/precommit per validator
- AppHash divergence detection (BlockID grouping) — chain-agnostic
- Upgrade plan readout (via LCD `cosmos.upgrade.v1beta1`)
- Block-time average, Prometheus metrics

What is Injective-specific:
- The default endpoints baked into [config.go](internal/config/config.go) — replace via `--rpc`/`--lcd`/`CMTOP_RPC`/`CMTOP_LCD`
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

The binary refuses to start with a non-loopback bind and an empty token.

### Compare AppHash across multiple RPCs

```sh
./bin/cmt-top \
  --rpc http://primary:26657 \
  --monitored-rpc http://node-a:26657 \
  --monitored-rpc http://node-b:26657
```

The divergence panel will surface any cross-endpoint AppHash mismatch in addition
to in-round vote-group splits.

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

The image is multi-stage: pnpm builds the SPA, Go builds the binary, the runtime
stage is a small `alpine` with only `ca-certificates` and the binary. Default
`CMD` runs `--mode web` on `0.0.0.0:8080`.

### Build

```sh
docker build -t cmt-top:local --build-arg VERSION=$(git describe --tags --always) .
```

### Run with `docker compose`

```sh
cp .env.example .env             # edit endpoints / token if exposing externally
docker compose up -d
docker compose logs -f cmt-top
```

The compose file binds ports to `127.0.0.1` by default — drop the prefix in
[docker-compose.yml](docker-compose.yml) only when `CMTOP_WEB_TOKEN` is set.

To use a TOML config inside the container, uncomment the `volumes:` and
`command:` lines in [docker-compose.yml](docker-compose.yml) and mount your
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

Scrape config lives at [deploy/prometheus.yml](deploy/prometheus.yml).

---

## Endpoints

| Path | Description |
|---|---|
| `GET /` | Svelte SPA (cold-load). |
| `GET /api/state` | Snapshot for first paint (chain card, validators, divergence). |
| `GET /api/validators?search=<q>` | Server-side filter. |
| `GET /api/validators/{addr}` | Single validator drilldown. |
| `GET /api/divergence` | Live divergence rounds. |
| `GET /api/divergence/history` | Recent resolved rounds. |
| `GET /api/blocks` | Block-time series. |
| `GET /api/chain` | Chain card + upgrade plan. |
| `WS  /ws?token=<t>` | Streaming envelope (snapshot + patches). |
| `GET /healthz` | Process liveness. |
| `GET /readyz` | First snapshot built. |
| `GET /metrics` | Prometheus (separate listener, default `127.0.0.1:9091`). |

---

## Development

```sh
make build         # web bundle + Go binary
make build-no-web  # binary only (web returns 503 from a stub)
make web           # Svelte SPA into internal/web/dist
make test          # Go unit tests
make test-race     # with -race
make e2e           # hits public Injective RPC; requires CMTOP_E2E=1
make lint          # golangci-lint --fix
```

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
                              (subscribes + emits back)
```

The orchestrator is the only goroutine that mutates `state.State`. The bus is
lossy: slow subscribers (a stuck WS client, a paused TUI) drop events with a
metric counter, never block the publisher.

## License

MIT.
