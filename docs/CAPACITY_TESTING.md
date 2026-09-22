# Capacity implementation and verification

The capacity implementation adds configurable admission, shared stream encoding, bounded
queues, compact revisioned investigations, conditional polling, and lightweight
investigation subscriptions. It retains one upstream collector per process.
The application default is 256 WebSocket connections, including handshakes in
progress. This is an admission bound, not a certification of production capacity.

The September 22 local checks passed the Go race detector/vet, 67 frontend unit
tests and seven Chromium scenarios. Recorded load checks include 200 sessions
held for five minutes, and 150 investigators over 128 rounds with the app, source
and driver sharing one CPU and 512 MiB. The comparable dashboard workload used
76.84% fewer stream payload bytes. See the [results and limitations](../testdata/capacity/results/README.md),
including the earlier failed simultaneous-start attempt and stricter reruns.

## Reproduce the local checks

From the repository root, with Go, Node, pnpm and Python 3 available:

```sh
go test -mod=readonly -race -timeout=10m ./...
go vet ./...
make capacity-smoke
make capacity-test PROFILE=testnet5 USERS=150 HOLD=30m
make capacity-test PROFILE=stalled128 USERS=150 HOLD=30m
make capacity-test PROFILE=mainnet45 USERS=200 HOLD=5m
make capacity-test PROFILE=mixed USERS=150 HOLD=1h
```

`capacity-smoke` builds the bundled application and the replay/load tools, then
runs 150 protocol sessions for ten seconds after a three-second ramp. Half the
sessions investigate; the other half consume the dashboard stream. Each run
prints its artifact directory containing JSON results, fixture/app logs and the
synthetic configuration. Set `CAPACITY_OUTPUT_DIR` to preserve results at a chosen
path. `CAPACITY_RAMP=0s` exercises simultaneous admission. The longer commands
above are explicit release checks; their presence does not mean they have passed.

The fixture has 45- and 5-validator profiles, duplicate/conflicting/nil/late votes,
120-block history and configurable stalled rounds. Independent tests also force
source reconnection and verify resubscription and retained evidence. The load
driver checks baseline-first sequencing, context profile negotiation, conditional
report reads, drop counters and evidence against an independent oracle. It
refuses public URLs, redirects and a target whose configured source does not
match the synthetic fixture.

For an enforced network boundary:

```sh
make capacity-isolated
```

This builds the protocol fixture and runs it with Docker `--network none`, two
CPUs and 1 GiB shared by the application, source and load generator. The image is
Go-only; it does not measure rendering, TLS or Gateway overhead. CI runs this
short smoke. The manually dispatched **Capacity soak** workflow runs the three
profiles for 5 minutes, 30 minutes or an hour and saves results even on failure.
`stalled128` uses 100% investigators; ordinary profiles default to 50%.
The isolated smoke preserves its results in `capacity-results/smoke` on the host.
Building the image needs dependency access; the actual workload has no outbound
network. Do not use `make run` or ordinary Compose as a synthetic test: their
defaults connect to a real chain.

Browser checks use the bundled build plus Chromium:

```sh
make capacity-build
cd web
pnpm exec playwright install chromium
pnpm test:e2e
```

The browser suite exercises the actual SPA against a loopback replay stack.
Fake-clock/socket tests separately cover transport faults and capability rollback.
The rollback test builds pinned pre-capacity revision
`52532c264714ce5fd7c10ab2e44c335e4a4fdbcc`; that commit must be available locally
(CI uses a full fetch). Headless visibility tests inject visibility events into
the real SPA; they do not substitute for operating-system tab behavior under load.
See the checked-in result notes under `testdata/capacity` for what was actually
run and its limitations.

## Operational bounds

| Resource | Initial bound / behavior |
| --- | --- |
| Browser connections | `ui.web.max_clients` / `CMTOP_WEB_MAX_CLIENTS`, default 256; active plus pending handshakes |
| API requests | `ui.web.api_rate_limit` / `CMTOP_WEB_API_RATE_LIMIT`, default 600 per second; separate normal, session-probe and handshake global lanes; API lanes also check resolved client IP |
| Forwarded identity | Only CIDRs in `ui.web.trusted_proxies` / `CMTOP_WEB_TRUSTED_PROXIES`; walk `X-Forwarded-For` from the trusted peer right to left; default trusts no proxy |
| Client send queue | 256 messages, 4 MiB logical bytes, two-second age/write deadline; disconnect persistent slow readers |
| Stream snapshots | One ordinary full snapshot per second; investigations opt into smaller context snapshots; divergence-only subscriptions receive one repair per second |
| Resync | Shared fresh baseline build at most once per 250 ms |
| Client commands | 4 KiB frames, 100 per second with a 512-command burst |
| Report cache | 64 MiB charged retained size, 32 heights, eight encoded variants per entry; materialized objects charged conservatively at three times full JSON size |
| Report work | Four builders shared globally; 256 interactive requests, eight legacy full reads and two captures admitted concurrently |
| Active report cache | Up to 250 ms evidence coalescing; correctly identified old revision; commit and eviction boundaries invalidate immediately |
| Polling | Active near one second, settled near five seconds, jitter/error backoff and `Retry-After`; hidden/manual pause suspend automatic reads |
| Pause/export | Fresh complete capture after work admission; successful paused capture remains local and immutable |

Cache charges and logical queue bytes are accounting bounds, not exact RSS limits.
Shared stream buffers are counted for every queued recipient. Temporary builders,
in-flight responses and the underlying archive need additional memory. Inspect
process RSS and CPU alongside these metrics when sizing a pod.

The new metric families are `cmt_top_browser_*`, `cmt_top_report_*`,
`cmt_top_capacity_work_seconds` and `cmt_top_http_request_seconds`. Labels are
bounded event/message/operation/route/status names. Continue monitoring upstream
ingestion and internal bus drop counters independently. No profiling route is
added to the public listener.

## Deployment gate

Run representative tests through the intended Gateway before claiming 150 active
production users. Confirm scheduling room, one scrape per pod, sustained CPU/RSS,
network throughput, active/settled evidence freshness, capture bursts, a real
browser cohort, and a longer mixed soak. The current short protocol tests cannot
measure all of these. Keep one replica while the archive remains process-local.

The deployment work and staged testnet/mainnet release remain in
[the implementation backlog](CAPACITY_IMPLEMENTATION_PLAN.md). In particular,
Asia's previously observed scheduling shortage and SigNoz namespace discovery
must be resolved before adopting larger resource requests. Do not infer working
scrapes from annotations alone. Rollback to the old application restores its
32-client cap; restart and rollback both discard the in-memory archive. Export
needed incident evidence before either action.
