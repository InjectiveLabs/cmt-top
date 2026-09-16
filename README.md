# cmt-top

A live consensus dashboard for CometBFT chains, in your terminal or browser.
Watch validators, voting power, block times, and RPC health. When a block stalls,
compare prevote and precommit hashes across rounds to find which validators
disagree.

Defaults connect to Injective. You can point it at your own chain or sentry.

## Run it

Build with Go 1.25.9+, Node.js 20, and pnpm 10.8.1:

```sh
git clone https://github.com/InjectiveLabs/cmt-top.git
cd cmt-top
make build
./bin/cmt-top              # terminal dashboard
./bin/cmt-top --mode web   # http://127.0.0.1:8080
```

Use a different RPC:

```sh
./bin/cmt-top --rpc http://your-node:26657 --mode web
```

## Docker

Compose uses `public.ecr.aws/l9h3g6c6/cmt-top:latest` after the first stable
release. Until then, [build the image locally](docs/USAGE.md#build).

```sh
cp .env.example .env
docker compose up -d
```

Open [localhost:8080](http://localhost:8080). Add `--profile metrics` to also run
Prometheus and Grafana. See the [Docker guide](docs/USAGE.md#docker) for local
builds and configuration.

## Investigate a stuck block

Click **Investigate block** in the dashboard. Pin the height, follow its rounds,
and compare hash groups and individual validators against an earlier round.
Pause to inspect the evidence, then export JSON before restarting the monitor.

The [screenshot walkthrough](docs/investigate-block/README.md) includes a runnable
demo. The [investigation reference](docs/BLOCK_INVESTIGATION.md) explains coverage,
retention, and how to interpret the votes. Hash splits help identify operators
to check; they do not tell you which binary a validator runs.

## Configuration

Settings apply in this order: defaults → TOML → `CMTOP_*` environment variables
→ CLI flags. Start with [config.example.toml](config.example.toml) or
[.env.example](.env.example). Set `CMTOP_WEB_TOKEN` before exposing the dashboard.

See [configuration, recipes, and API endpoints](docs/USAGE.md).

## Development

```sh
make test-race
cd web && pnpm install --frozen-lockfile && pnpm check && pnpm test
```

CI runs on Blacksmith. Version tags trigger release builds and Docker publishing
to public ECR; stable releases also update `latest`. See [CI setup](docs/CI.md).

MIT licensed. Inspired by [tmtop](https://github.com/QuokkaStake/tmtop) and
[pvtop](https://github.com/blockpane/pvtop).
