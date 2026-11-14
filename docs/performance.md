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

The gap between 2.3 ms and 10.3 ms is process start-up, not the work: the run does three allowlisted GETs over TLS with a fresh connection each, and renders one document. Nothing here is close to the 200 ms budget, which leaves a generous allowance for a slow CI runner and for a larger cluster.

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
| `NodesReport5000` (shipped path) | **207 ms** | 20.0 MB | 26.9 k |
| `NodesReportPaged5000` (10 pages, wire only) | 8.3 ms | 3.2 MB | 11.4 k |
| `NodesWire5000` (allowlist + TLS, no decode) | 0.78 ms | 1.04 MB | 1.15 k |
| `NodesJSONDecode5000` (`encoding/json`, report fields) | 2.2 ms | 1.08 MB | 10.3 k |

The allowlist wrapper allocates once per request because it copies the request to set `Connection: close`; that is the largest single line in the micro-benchmarks and it is deliberate (a `RoundTripper` may not modify its argument). The parser is linear in the number of checks and bounded at 256.

### Binary size

`go build -trimpath -ldflags "-s -w -buildid="`, `CGO_ENABLED=0`, no version stamp:

| Target | Size |
| --- | --- |
| `linux/amd64` | 26.4 MiB (27,717,756 B) |
| `linux/arm64` | 24.6 MiB (25,755,772 B) |
| `darwin/arm64` | 25.2 MiB (26,421,090 B) |

Most of that is `client-go` plus `k8s.io/api`: the client `Scry` builds carries the typed core/v1 client, which pulls the API machinery and the openapi tree behind it.

## Open finding: the 5,000-node node list

`NodesReport5000` is 207 ms for one report — about 80× the cost of everything else in a `status` run — because the typed client decodes the whole `NodeList` through client-go's scheme machinery (~20 MB allocated for a ~600 KB answer). The wire half of the same answer costs 0.78 ms and `encoding/json` into the eight fields the report actually reads costs 2.2 ms.

A measured alternative, applied to a scratch copy of the tree only:

| | Today | Raw decode |
| --- | --- | --- |
| `NodesReport5000` | 207 ms, 20.0 MB | **6.5 ms, 3.8 MB** |
| `darwin/arm64` binary | 25.2 MiB | **10.6 MiB** |
| `internal/kube` test suite | green | green |

That change touches `internal/kube/observe.go` and `config.go`, which this branch's backend does not own, so it is recorded here and handed to the code owner rather than applied. Two things a future change must carry with it: the request body cap (64 KiB today, for `/readyz` and `/version`) has to rise for the node list or a large cluster is truncated, and the 401/403 classification has to move from `apierrors` to the HTTP status, which the scratch copy did and the black-box probe accepted unchanged.

With the current decode, a cluster of roughly 5,000 nodes would put `status` at the edge of its own budget. Smaller clusters are far inside it: the node list is decoded at about 41 µs per node, so 100 nodes is ~4 ms.

## How to rerun

```sh
go test -run=^$ -bench . -benchmem ./internal/kube           # benchmarks
go test -run 'TestStatusStaysUnderBudget|TestStatusLeavesNoGoroutineBehind' -v ./internal/cli
go test -run 'TestStatusProcessBudget|TestBinarySizePerTarget' -v ./cmd/scry
```

Add `-short` to skip the process-level measurements (cross-compiles and the real binary); CI runs them in full. The process test logs wall time, peak RSS and each binary size with `t.Logf`, so `-v` is what makes the numbers visible.

Peak RSS comes from `ProcessState.SysUsage().(*syscall.Rusage).Maxrss`, normalised to bytes from kilobytes on Linux and from the native byte count on Darwin. No new dependency is involved.
