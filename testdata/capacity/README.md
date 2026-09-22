# Synthetic capacity profiles

The executable fixture is `internal/testutil/replay`, with deterministic seed 1
by default. It generates 40-character consensus addresses, 64-character block
hashes and matching LCD metadata; no captured chain data or live chain access is
required. `mainnet45` has 45 validators and `testnet5` has five. Both start with
120 committed history events and produce two vote phases, differing hash groups,
nil votes, an exact duplicate, a conflicting observation and delayed votes.
Stalled profiles then add one late prevote per second in successive retained
rounds, forcing evidence/cache invalidation under investigation load; subsequent
passes repeat the same evidence to exercise 304 responses.

Build from the repository root:

```sh
go build -o bin/cmt-top ./cmd/cmt-top
go build -o bin/cmt-top-replay ./cmd/cmt-top-replay
go build -o bin/cmt-top-load ./cmd/cmt-top-load
bash deploy/capacity/smoke.sh
CAPACITY_USERS=200 CAPACITY_RAMP=0s bash deploy/capacity/smoke.sh
CAPACITY_STALLED=1 CAPACITY_ROUNDS=32 CAPACITY_INVESTIGATE_PERCENT=100 bash deploy/capacity/smoke.sh
CAPACITY_STALLED=1 CAPACITY_ROUNDS=128 CAPACITY_INVESTIGATE_PERCENT=100 bash deploy/capacity/smoke.sh
CAPACITY_PROFILE=testnet5 bash deploy/capacity/smoke.sh
```

The script scrubs inherited environment configuration and writes every RPC/LCD
endpoint explicitly. The runner refuses non-loopback targets, redirects and an
app whose chain identity or active source differs from its required fixture.
For an operating-system network boundary, build and run the protocol harness:

```sh
docker build -f deploy/capacity/Dockerfile -t cmt-top-capacity .
docker run --rm --network none cmt-top-capacity
```

The container intentionally uses the Go-only app: this measures the real
collector/API/WebSocket implementation but not SPA rendering. Browser checks
must use the webui build independently. A 10-second smoke pass does not certify
150 production users; run the documented ramp/30-minute hold/soak through the
deployment gateway and validate actual browsers as separate release gates.

The driver probes `/api/session` before each WebSocket, falling back to full
`/api/state` on an old server's 404 (`-legacy-bootstrap` explicitly tests the old
client path). It checks the authoritative initial snapshot and every sequence number, and polls investigation
reports with conditional requests if the baseline advertises compact support.
Investigators negotiate context-only subscriptions on capable servers. Every
intended investigator must cross the pong barrier and obtain a validated report;
one successful session cannot hide another stalled session. HTTP 200 reports
must match the requested height, representation, schema/epoch and detail shape;
304 responses require a previously validated same-height ETag. The JSON
driver follows the latest observed height and polls once per second to sustain
an active-investigation workload. This deliberately exceeds settled-page polling
(five seconds) and does not replace actual browser lifecycle checks. The JSON
result includes offered/admitted/baseline-ready sessions, HTTP statuses, errors,
message/snapshot/context counts, bytes, latency quantiles, report/304 counts,
drop-metric deltas and source request counters. With metrics configured, initial
and final scrapes must succeed with finite counters, no missing original series
and no resets; new drop series count against the gate. Admission retries follow
transient network/429/503 failures (including Retry-After), stop on 401, and are
bounded by a ten-second per-session startup deadline. Transient failures, retries
and baseline-ready latency remain visible in JSON. One-second metric samples record
app CPU counter start/end, sampled peak RSS/goroutines/cache/queue bytes, and
the generator's sampled heap. `processMetricsAvailable=false` means app CPU/RSS
were unavailable on that platform, not zero usage. Latency quantiles use a
bounded 32,768-observation reservoir per route family during long soaks.
The runner freezes replay after load and compares the captured archive against an independent source-evidence oracle.

Observation timestamps, process identity, request context and cache revisions
are removed only for the semantic digest. Source vote timestamps and raw hash
evidence remain significant. `/_fixture` controls are
available only on the mock source; they are not application production routes.
`POST /_fixture/control` accepts `{"paused":true}` and/or `{"disconnect":true}`
for deterministic outage/recovery checks; restore with `{"paused":false}`.

`go test -race ./internal/testutil/replay` drives the actual orchestrator through
these wire protocols and checks source evidence, nil/conflicting/late votes,
LCD enrichment, 120-block history, 128-round bounding and eviction at 130 rounds.

Recorded development measurements and their limits are in [results/README.md](results/README.md).
