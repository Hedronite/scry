# Performance

What `scry` costs, what it is required to cost, and how to reproduce both.

## Budgets

These are contracts, not observations: both are asserted in the test suite.

| Budget | Where | How it is checked |
| --- | --- | --- |
| One `status` run against a loopback TLS fixture finishes in under **200 ms** | `internal/cli/perf_test.go`, `cmd/scry/perf_test.go` | median of 7 in-process runs and median of 5 process runs |
| `status` leaves **no goroutine** running after it returns | `internal/cli/perf_test.go` | `runtime.NumGoroutine` back to its pre-run baseline within a two-second settle window |

Everything else on this page is recorded, never asserted: a runner with a slower disk or a busier CPU must show up as a number to read, not as a red build.

## Measurement machine

A class, not a host: **Apple M4 Max**, 14 cores (10 performance, 4 efficiency), **macOS 26.5.1**, `arm64`, **Go 1.27.4**, `CGO_ENABLED=0` for size and process runs. Numbers from another machine will differ; the shape of the tables is what transfers.

## Measured now

### One `status` run

| Measurement | Value |
| --- | --- |
| In-process `status` (`internal/cli`), median of 7 | **2.3 ms** (min 1.8 ms, max 2.7 ms) |
| Real binary `status` (`cmd/scry`), median of 5 | **10.3 ms** (min 9.8 ms, max 12.1 ms) |
| Peak RSS of that process (`ProcessState.SysUsage`) | **25.0 MiB** |
| Goroutines after five runs | baseline 3, after 3 |

The gap between 2.3 ms and 10.3 ms is process start-up, not the work: the run does three allowlisted GETs over TLS with a fresh connection each, and renders one document. Nothing here is close to the 200 ms budget.

### Benchmarks

`go test -run=^$ -bench . -benchmem ./internal/kube`

| Benchmark | Time | Bytes | Allocs |
| --- | --- | --- | --- |
| `ParseReadyzVerbose/bare-ok` | 30.4 ns | 35 B | 2 |
| `ParseReadyzVerbose/8-checks` | 501 ns | 928 B | 6 |
| `ParseReadyzVerbose/64-checks` | 3.57 µs | 8.0 KB | 9 |
| `AllowlistRoundTrip/allowed-readyz-verbose` | 191 ns | 368 B | 3 |
| `AllowlistRoundTrip/allowed-nodes-paged` | 299 ns | 416 B | 4 |
| `AllowlistRoundTrip/refused-delete` | 149 ns | 208 B | 4 |
| `AllowlistRoundTrip/refused-watch` | 304 ns | 272 B | 6 |
| `AllowlistRoundTrip/refused-other-host` | 114 ns | 192 B | 4 |
| `NodesReport5000` (shipped path, one cold report) | **≈ 11 ms** | 20 MB | 28 k |

The allowlist wrapper allocates once per request because it copies the request to set `Connection: close`; that is the largest line in the micro-benchmarks and it is deliberate (a `RoundTripper` may not modify its argument). The parser is linear in the number of checks and bounded at 256.

### Reading a benchmark of the node list correctly

`NodesReport5000` is the only benchmark that talks to a rate-limited client. The client is built with the shipped `QPS 2 / burst 3`, so a *sequence* of reports in one process is paced about 500 ms apart once the burst is spent, and a multi-iteration average reports that pacing rather than the work:

| `-benchtime` | Reported per op | What it is |
| --- | --- | --- |
| `1x` in a fresh process | **≈ 11 ms** | one report: three requests are inside the burst |
| `4x` | 133 ms | three free, one paced |
| `5x` | 207 ms | three free, two paced |
| `8x` | 317 ms | three free, five paced |

A report costs about **11 ms for 5,000 nodes**; the larger figures are the deliberate client-side rate limiter, which `wait` depends on for its pacing and which `status` never reaches (a single run issues four requests at most, inside the burst). Use `-benchtime=1x -count=1` in separate processes for a cold per-report figure.

At 5,000 nodes the shipped path is **complete, not truncated**: `NodesReport5000` fails unless the report carries all 5,000 items, and it passes. The client issues one unpaginated `LIST`, which a stock apiserver answers in full.

### Wire cost, and the 64 KiB body cap

`internal/kube` reads non-node responses through a 64 KiB cap. The typed node list does not pass through that reader, but the two raw benchmarks do (`NodesWire5000` and `NodesReportPaged5000` call `get`), so their `MB/s` figures are the cap, not the body: each request reads the first 64 KiB of a ~600 KB answer. Their honest reading is a per-request cost of about **0.8–0.9 ms**, not a throughput. Reading the full answer requires a larger cap, which is one of the conditions of the v0.2 candidate below.

## v0.2 candidate: decode the node list without the typed client

Recorded, not scheduled, and explicitly not part of `v0.1.0`: replacing the typed core/v1 client with a minimal `encoding/json` decode touches the transport path, changes a response size cap and moves 401/403 classification, which is a security-sensitive change that should not ride a first-release PR.

Measured on a scratch copy of the tree (raw decode, node-list cap raised to 8 MiB, everything else untouched; `internal/kube` and `cmd/scry` green, including the black-box probe):

| | Today (typed client) | Raw decode |
| --- | --- | --- |
| One 5,000-node report | ≈ 11 ms, 20 MB, 28 k allocs | **≈ 6.3 ms, 3.8 MB, 11.5 k allocs** |
| Wire, full ≈ 600 KB answer | 0.8–0.9 ms at the 64 KiB cap | **1.26 ms, whole body** |
| `darwin/arm64` binary | 25.2 MiB | **10.6 MiB** |
| `internal/kube` + `cmd/scry` suites | green | green |

The win is binary size (about 58% smaller) and a modest latency and allocation reduction; it is not a defect fix. A change like this must carry two things:

1. **The node-list body cap.** 64 KiB truncates a large cluster's node list. The scratch copy raised it to 8 MiB for every response; a real change should size the cap per endpoint.
2. **401/403 by HTTP status.** `apierrors.IsUnauthorized` / `IsForbidden` go away with the typed client, so the classification moves to the status code. The scratch copy did this and the black-box probe accepted it unchanged.

## Binary size

`go build -trimpath -ldflags "-s -w -buildid="`, `CGO_ENABLED=0`, no version stamp:

| Target | Size |
| --- | --- |
| `linux/amd64` | 26.4 MiB (27,717,756 B) |
| `linux/arm64` | 24.6 MiB (25,755,772 B) |
| `darwin/arm64` | 25.2 MiB (26,421,090 B) |

Most of that is `client-go` plus `k8s.io/api`, which the v0.2 candidate above would remove.

## How to rerun

```sh
go test -run=^$ -bench . -benchmem ./internal/kube           # benchmarks
go test -run=^$ -bench 'NodesReport5000$' -benchtime=1x -count=1 ./internal/kube   # one cold report
go test -run 'TestStatusStaysUnderBudget|TestStatusLeavesNoGoroutineBehind' -v ./internal/cli
go test -run 'TestStatusProcessBudget|TestBinarySizePerTarget' -v ./cmd/scry
```

Add `-short` to skip the process-level measurements (cross-compiles and the real binary); CI runs them in full. The process test logs wall time, peak RSS and each binary size with `t.Logf`, so `-v` is what makes the numbers visible.

Peak RSS comes from `ProcessState.SysUsage().(*syscall.Rusage).Maxrss`, normalised to bytes from kilobytes on Linux and from the native byte count on Darwin. No new dependency is involved.
