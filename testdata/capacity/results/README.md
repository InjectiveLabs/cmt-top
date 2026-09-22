# Recorded capacity checks — 2026-09-22

These are local development measurements from deterministic replay, not a
production capacity certification. No workload contacted a chain or production
application. Files replace ephemeral local ports and omit generated configs/logs.
Each JSON records its environment and workload; native runs had no CPU/memory
quota, and the app, fixture and generator shared the machine. Docker runs used
`--network none` and shared the stated resource quota across all three processes.
Other local development activity can affect the timings.

| Run | Sessions | Ramp + hold | WS / report MB | Report p99 | App peak RSS | Result |
| --- | ---: | ---: | ---: | ---: | ---: | --- |
| [Old HEAD dashboard](old-head-dashboard10.json) | 10 | 3 + 10 s | 64.63 / 0 | — | unmeasured | pass |
| [Updated dashboard](updated-dashboard10.json) | 10 | 3 + 10 s | 14.97 / 0 | — | 34.75 MB | pass |
| [Updated mixed](updated-mixed150.json) | 150 | 3 + 20 s | 243.70 / 63.51 | 4.53 ms | 48.09 MB | pass |
| [Stalled 128 rounds](updated-stalled128-150.json) | 150 | 3 + 20 s | 120.86 / 739.39 | 29.11 ms | 136.97 MB | pass |
| [Initial native burst](native-burst200-failed.json) | 168 / 200 | 0 + 10 s | 104.90 / 32.63 | 10.71 ms | 50.76 MB | **failed** |
| [Strict native burst](strict-native-burst200.json) | 200 | 0 + 12 s | 146.47 / 45.38 | 10.35 ms | 52.15 MB | pass |
| [Strict stalled 128 rounds](strict-native-stalled128-150.json) | 150 | 3 + 10 s | 119.69 / 398.69 | 26.38 ms | 147.90 MB | pass |
| [Isolated 2 CPU / 1 GiB](isolated-200-5m.json) | 200 | 3 + 300 s | 5,441.69 / 1,131.95 | 47.57 ms | 56.09 MB | pass |
| [Strict isolated 2 CPU / 1 GiB](isolated-150-strict-smoke.json) | 150 | 3 + 10 s | 107.25 / 35.62 | 17.98 ms | 40.05 MB | pass |
| [Strict stalled, 1 CPU / 512 MiB](isolated-stalled128-1cpu.json) | 150 | 3 + 20 s | 122.37 / 732.91 | 105.48 ms | 118.46 MB | pass |

MB are decimal bytes; RSS/cache/queue peaks are sampled once per second, so short
peaks can be missed. CPU is process CPU time divided by wall time, expressed as
cores. Generator heap is recorded separately; native runs do not isolate its CPU
cost. These checks measure protocol delivery and report work, not TLS, ingress,
real network latency, rendering or browser memory. Report latency observations
also include the final oracle capture, which is not counted as a session report.

The before/after dashboard comparison used the same 45-validator seed, 120-block
history, one round, 1 ms event spacing, one-second block spacing and ten sessions.
The old binary came from immutable revision
`52532c264714ce5fd7c10ab2e44c335e4a4fdbcc` via a separate temporary archive. The
updated binary came from this working tree. WS bytes fell **76.84%**; both final
captures produced the same normalized evidence digest. Baseline bootstrap differs
intentionally: old clients fetch full HTTP state, while capable clients probe the
small session endpoint before receiving the authoritative WS baseline.

Mixed runs use 50% investigators; stalled runs use 100%. Investigators follow the
current height once per second, which is more active than a settled investigation
page's five-second cadence. The source intentionally produces persistent hash
splits and conflicts rather than assuming healthy-chain traffic. Stalled profiles
finish all 128 rounds before clients start, then add a late vote once per second
across retained rounds. The resulting large report/network totals are relevant
when sizing egress; low CPU alone does not establish deployment capacity.

All passing runs recorded zero sequence gaps, transport/session errors and drop
counter increases, one upstream WS connection before and after load, and a
matching independent full-report evidence oracle. The old-HEAD, updated native and failed native-burst files
predate the final per-session coverage and response-validation gates. Their
aggregate report/pong/drop counts were reviewed, but those runs did not enforce
that every investigator received a usable report. In particular, the five-minute
isolated result predates those stricter gates. The later strict runs require every
intended investigator to cross its profile barrier and validate at least one
report, require correct 304 provenance, and fail on unavailable, missing, reset or
nonfinite final drop counters. All strict runs passed those gates, including the final isolated stalled run
under the current 1 CPU / 512 MiB deployment-sized budget shared by the app,
fixture and generator. Its short duration still does not establish soak safety.

The initial 200-session simultaneous native run failed on 32 TCP connection
resets during bootstrap/handshake, with no HTTP admission rejection or app crash.
The runner at that point did not model the browser's bounded startup recovery.
The failure is retained here rather than removed from the record. The later
runner exposes transient errors/retry counts, honors Retry-After and enforces a
ten-second startup deadline. The strict 200-session rerun admitted all sessions
without needing a retry; this does not prove the earlier host backlog condition
was eliminated. Integration tests separately exercise retry recovery, deadlines
and terminal authentication failure.

A gateway run, real browser cohort, longer mixed soak, sustained egress budget,
cache/queue headroom and working per-pod scrapes remain deployment acceptance
work. See [CAPACITY_TESTING.md](../../../docs/CAPACITY_TESTING.md) for reproducible
commands and operational limits.
