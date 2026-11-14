# Scry

Scry is a small, inspect-only command-line tool that answers one question about a Kubernetes cluster: **is it ready, and if not, what is not?**

It is a companion to [hedronetes-h3s](https://github.com/VirtualMachinist/hedronetes-h3s) and works against stock Kubernetes too. It is not an operator or a control plane: it reads and reports, and it is built so it cannot write.

> **Status:** v0.1.0, pre-1.0. The contract — the `scry.status/v0`, `scry.config/v0` and `scry.version/v0` documents, the exit codes and the request allowlist — changes only additively. Releases are reproducible and carry build provenance (see [Install](#install)).

## What Scry does

- Reports API server reachability, `/readyz`, and node readiness as a table or as JSON (`scry.status/v0`).
- Waits, with bounded polling, until a cluster is ready (`scry wait --for=readyz`).
- Treats a cluster you have declared *expected dark* as healthy when it is unreachable.

## What Scry does not do

- No create, update, patch, delete, or apply. Every request passes a local allowlist of read-only `GET` paths; anything else is refused before it reaches the network.
- No watches and no long-lived connections: `wait` polls with bounded backoff, one fresh request at a time.
- No in-cluster mode and no implicit kubeconfig: you pass the path explicitly.
- No exec-based or auth-provider credentials, and no skipping TLS verification.

## Install

Download an archive for your platform from the [releases page](https://github.com/Hedronite/scry/releases): `linux/amd64`, `linux/arm64` or `darwin/arm64`. Each release lists the archives, a `SHA256SUMS` file, and a build provenance attestation per archive.

```console
$ VERSION=0.1.0 OS=linux ARCH=amd64        # or: darwin arm64, linux arm64
$ BASE=https://github.com/Hedronite/scry/releases/download/v$VERSION
$ curl -fsSLO $BASE/scry_${VERSION}_${OS}_${ARCH}.tar.gz
$ curl -fsSLO $BASE/SHA256SUMS

# 1. the checksum (Linux: sha256sum; macOS: shasum -a 256)
$ grep " scry_${VERSION}_${OS}_${ARCH}.tar.gz\$" SHA256SUMS | sha256sum -c -

# 2. the provenance: this archive was built by this repository's release workflow
$ gh attestation verify scry_${VERSION}_${OS}_${ARCH}.tar.gz --repo Hedronite/scry

$ tar xzf scry_${VERSION}_${OS}_${ARCH}.tar.gz
$ ./scry_${VERSION}_${OS}_${ARCH}/scry version
scry v0.1.0 (<commit>)
```

The archive holds `scry`, `LICENSE` and `README.md`, nothing else. Scry is one static binary with no runtime dependencies.

**Reproducible.** The binaries are built without cgo, with `-trimpath` and an empty build id, by the Go version `go.mod` names, and carry no build date, host name or path. Check out the tag and run `bash hack/release.sh v0.1.0`: every file in `dist/` hashes to the line in the release's `SHA256SUMS` (CI builds twice in two directories and fails if they differ). A binary built any other way prints `scry devel`.

## Getting started

```console
$ scry status --kubeconfig ./lab-one.kubeconfig

cluster:    lab-one (expected light)
as of:      2026-11-13T20:00:00Z
server:     v1.34.0+h3s (h3s)
apiserver:  up, http 200, 18ms
readyz:     verbose, ok
            ok    etcd
            ok    ping
nodes:      1 of 2 ready
            ready     node-a
            NOT READY node-b (RuntimeNotReady, network unavailable)
severity:   YELLOW
reasons:    node_not_ready, network_unavailable
```

Add `--json` for the machine-readable `scry.status/v0` document (see [`schema/scry.status.v0.json`](schema/scry.status.v0.json)); add `--quiet` for the exit code only. Wait for readiness with `scry wait --for=readyz` (below).

### Quickstart with a config file

Name your clusters once in a TOML file (paths and expectations only, never credentials) and select one by alias:

```toml
# scry.toml
schema = "scry.config/v0"

[[cluster]]
alias = "lab-one"
kubeconfig = "./lab-one.kubeconfig"
expected = "light"
timeout = "10s"        # per request
```

```console
$ scry status --config ./scry.toml --cluster lab-one
$ scry wait --for=readyz --config ./scry.toml --cluster lab-one --timeout 2m
$ scry status --config ./scry.toml --json      # one entry in the file: --cluster is optional
```

Scry never reads a config unless you pass `--config`, and never reads a kubeconfig you did not point it at.

## How Scry talks to a cluster

- **Kubeconfig:** required, by explicit path (`--kubeconfig`). `$KUBECONFIG` is honoured only when you also pass `--allow-env-kubeconfig`. `--context` selects a context; a missing one is refused, listing the contexts the file actually has.
- **Authentication:** client certificates and static bearer tokens (including `tokenFile`). Exec-based and `auth-provider` credentials are refused at load, before any network traffic.
- **TLS:** verification is always on. `insecure-skip-tls-verify` is refused; there is no unsafe flag.
- **Allowlist:** every request is checked against this table, and refused locally if it is not on it. Redirects are refused.

| Method | Path                  | Query keys        |
| ------ | --------------------- | ----------------- |
| GET    | `/readyz`, `/livez`   | `verbose`         |
| GET    | `/version`            | —                 |
| GET    | `/api/v1/nodes`       | `limit`, `continue` |
| GET    | `/api/v1/nodes/{name}` | —                |

## Expected dark

An apiserver port that is dark on purpose (for example, a lab cluster you only reach from elsewhere) is not a failure. Declare it once:

```console
$ scry status --kubeconfig ./lab-one.kubeconfig --expected dark

cluster:    lab-one (expected dark)
as of:      2026-11-13T20:00:00Z
apiserver:  down, expected dark
severity:   GREEN
reasons:    expected_dark
```

`scry wait` refuses to wait on a declared-dark cluster (exit 3, reason `wait_expected_dark`, zero requests made): nothing can make a documented-dark apiserver pass `readyz`. Pass `--expected light` if the cluster is supposed to come up.

## Output

`scry status` prints a table (above); `--json` prints one `scry.status/v0` document instead; `--quiet` prints nothing at all — it silences stdout and stderr, and only a usage error (exit 8) prints one line. The exit code is the answer; scripts and agents should read the JSON form.

The final document of `scry wait` adds a `wait` row:

```text
wait:       ready after 3 polls, 1.8s
```

When Scry cannot get a verdict from the wire — a config problem, refused credentials, a TLS failure, or a connected-but-silent peer — the document still validates, `observed` is `false`, `apiserver.result` is `"unknown"` (**no verdict from the wire**, not a statement about the cluster), and the exit code says what happened. `scry wait` may also time out on such a last poll; `reasons` then starts with `timeout`.

### Exit codes

| Code | Meaning                                              |
| ---- | ---------------------------------------------------- |
| 0    | observed, GREEN (includes expected dark)             |
| 1    | observed, YELLOW                                     |
| 2    | observed, RED; also a `wait` timeout                 |
| 3    | config: bad kubeconfig, refused auth kind, missing context, wait on a declared-dark cluster |
| 4    | auth: credentials rejected                           |
| 5    | TLS: verification failed                             |
| 6    | transport: connected, then the peer went silent      |
| 7    | internal error (including an allowlist refusal)      |
| 8    | usage: bad flags; no document is emitted             |

### Reason codes

`reasons` in the document lists why the severity is what it is, or why there is no severity. Codes marked with an exit code are the cause of an unobserved document (`observed: false`).

| Reason | Exit | Meaning |
| --- | --- | --- |
| `expected_dark` | — | apiserver unreachable, and declared expected dark: GREEN |
| `apiserver_down` | — | TCP connection failed on a cluster expected to be up: RED |
| `readyz_failed` | — | `/readyz` did not return 200: RED |
| `no_nodes` | — | the node list is empty: RED |
| `node_not_ready` | — | some or all nodes are not Ready (or Unknown): YELLOW, or RED when none is ready |
| `network_unavailable` | — | a node reports `NetworkUnavailable=True`: YELLOW |
| `timeout` | — | `wait` ran out of time; exit 2 |
| `wait_expected_dark` | 3 | `wait` refused a cluster declared expected dark |
| `config_invalid` | 3 | the config file is unreadable, invalid or names no such cluster |
| `kubeconfig_missing`, `kubeconfig_invalid` | 3 | no kubeconfig given, or it cannot be used (including a missing context) |
| `exec_auth_refused`, `auth_provider_refused`, `insecure_tls_refused` | 3 | the kubeconfig asks for a credential kind or setting Scry refuses |
| `out_write_failed` | 3 | `--out` could not be written; the old file is untouched |
| `auth_rejected`, `rbac_denied` | 4 | 401, or 403 (listing nodes needs `get` and `list` on `nodes`) |
| `tls_verify_failed`, `tls_cert_expired` | 5 | the server certificate could not be verified |
| `http_unresponsive` | 6 | connected, then the peer reset or went silent |
| `allowlist_denied`, `internal` | 7 | a bug: Scry asked for something it must not, or failed unexpectedly |

## Exporting to Vigil

`scry status --out PATH` also writes the full `scry.status/v0` document to `PATH` (temp file in the same directory, fsync, atomic rename, mode `0644`), whatever stdout shows. A timer runs it every minute and [Vigil](https://github.com/Hedronite/vigil) reads the file; Vigil never runs Scry or holds a kubeconfig. The file is written on exits 0, 1, 2, 4, 5 and 6, never on 3, 7 or 8 (the last file stays and Vigil ages it into stale); a failed write is exit 3, reason `out_write_failed`. One file feeds Vigil one cluster today. See [docs/vigil.md](docs/vigil.md) for the mapping to Vigil's `scry{}` block, and [docs/examples](docs/examples) for systemd and launchd timers.

## Waiting

`scry wait --for=readyz` polls until ready or `--timeout` (default `60s`) expires. Every poll observes the cluster once, with each connection closed after use so a rotated certificate is picked up on the next poll. The schedule is bounded backoff: 500ms growing 1.5x to a 5s cap, no jitter.

- **Ready** means an observed poll with `/readyz` ok and the apiserver `up`. Exit 0, or 1 when ready but degraded.
- **Timeout** keeps the last poll's document, adds reason `timeout`, and always exits 2.
- **Fail fast:** config (3), auth (4), TLS (5) and internal (7) failures end the wait immediately — waiting cannot heal them.
- **Keep polling:** an unreachable (down) or silent peer keeps polling until the timeout.
- **Interrupt:** Ctrl-C ends the process by default — no cleanup, no summary document.

## What "ready" means

- **API server:** `up` means the peer answered (even with a 401 or a certificate failure — the endpoint exists and speaks), `down` means the TCP connection failed, `unknown` means no verdict from the wire (see Output above).
- **Readyz:** on stock Kubernetes, Scry parses `?verbose` sub-checks when the server offers them. On h3s, `/readyz` and `/livez` return a hard-coded `ok` before authentication, with no sub-checks, so the node conditions are the real readiness signal there; Scry reports that bare `ok` as `aggregate` and prints "no sub-checks reported" rather than inventing detail. A bare `ok` proves the API server process answers — it does not prove workloads can run.
- **Nodes:** the `Ready` condition of every node in `/api/v1/nodes`, plus `NetworkUnavailable`. A node whose `Ready` condition is neither True nor False is reported with `unknown: true` (rendered `UNKNOWN`, counted as not ready). All nodes not Ready — or all without a verdict — is RED; some is YELLOW. Scry never dials kubelet ports and never hardcodes component names.

## Configuration file

Scry reads no config file unless you name one with `--config`. The file is TOML (`scry.config/v0`), holds paths and expected state — never credentials — and is validated strictly:

```toml
schema = "scry.config/v0"

[[cluster]]
alias = "lab-one"
kubeconfig = "./lab-one.kubeconfig"
expected = "dark"
timeout = "2m"
```

Resolution: `--config` with `--cluster` selects that cluster's entry (kubeconfig path, expected state, per-request timeout). With exactly one entry in the file, `--cluster` can be omitted; several entries without `--cluster` are refused, listing the aliases. Flags always beat the entry: `--kubeconfig` and `--expected` override whatever the file says. The entry's `timeout` limits each request; `wait --timeout` limits the whole run and has no config equivalent. `--cluster` without `--config` only overrides the report's alias.

## Building from source

```console
$ go build ./cmd/scry
```

Go 1.27 or newer (the exact version is pinned in `go.mod`). `bash hack/release.sh vX.Y.Z` builds the release assets locally. Performance budgets and measurements: [`docs/performance.md`](docs/performance.md). Pinned versions and their reasons are recorded in [`docs/versions.md`](docs/versions.md).

## Security promise

- Kubeconfig contents, tokens, server addresses and file paths never appear in output, error messages, or logs; cluster reports identify the cluster by alias only.
- Test fixtures are synthetic (`node-a`, `lab-one`, `example.invalid`); nothing real is committed.
- Vulnerabilities and security questions: see [SECURITY.md](SECURITY.md). Contributing (DCO): see [CONTRIBUTING.md](CONTRIBUTING.md).

## Licence

Apache License 2.0. See [LICENSE](LICENSE).

Copyright 2026 Evan Eugene Pincham.
