# Scry

Scry is a small, inspect-only command-line tool that answers one question about a Kubernetes cluster: **is it ready, and if not, what is not?**

It is a companion to [hedronetes-h3s](https://github.com/VirtualMachinist/hedronetes-h3s) and works against stock Kubernetes too. It is not an operator or a control plane: it reads and reports, and it is built so it cannot write.

> **Status:** alpha. The contract — the `scry.status/v0` document, the exit codes, and the request allowlist — is frozen. `scry status` and `scry wait` observe a real cluster; the Vigil export and signed releases come later.

## What Scry does

- Reports API server reachability, `/readyz`, and node readiness as a table or as JSON (`scry.status/v0`).
- Waits, with bounded polling, until a cluster is ready (`scry wait --for=readyz`).
- Treats a cluster you have declared *expected dark* as healthy when it is unreachable.

## What Scry does not do

- No create, update, patch, delete, or apply. Every request passes a local allowlist of read-only `GET` paths; anything else is refused before it reaches the network.
- No watches and no long-lived connections: `wait` polls with bounded backoff, one fresh request at a time.
- No in-cluster mode and no implicit kubeconfig: you pass the path explicitly.
- No exec-based or auth-provider credentials, and no skipping TLS verification.

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

## Waiting

`scry wait --for=readyz` polls until ready or `--timeout` (default `60s`) expires. Every poll observes the cluster once, with each connection closed after use so a rotated certificate is picked up on the next poll. The schedule is bounded backoff: 500ms growing 1.5x to a 5s cap, no jitter.

- **Ready** means an observed poll with `/readyz` ok and the apiserver `up`. Exit 0, or 1 when ready but degraded.
- **Timeout** keeps the last poll's document, adds reason `timeout`, and always exits 2.
- **Fail fast:** config (3), auth (4), TLS (5) and internal (7) failures end the wait immediately — waiting cannot heal them.
- **Keep polling:** an unreachable (down) or silent peer keeps polling until the timeout.
- **Interrupt:** Ctrl-C ends the process by default — no cleanup, no summary document.

## What "ready" means

- **API server:** `up` means the peer answered (even with a 401 or a certificate failure — the endpoint exists and speaks), `down` means the TCP connection failed, `unknown` means no verdict from the wire (see Output above).
- **Readyz:** on stock Kubernetes, Scry parses `?verbose` sub-checks when the server offers them. On h3s, `/readyz` answers a bare `ok` and nothing else; Scry reports that as `aggregate` and prints "no sub-checks reported" rather than inventing detail. A bare `ok` proves the API server process answers — it does not prove workloads can run.
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

Resolution: `--config` with `--cluster` selects that cluster's entry (kubeconfig path, expected state, wait timeout). With exactly one entry in the file, `--cluster` can be omitted; several entries without `--cluster` are refused, listing the aliases. Flags always beat the entry: `--kubeconfig`, `--expected` and `--timeout` override whatever the file says. `--cluster` without `--config` only overrides the report's alias.

## Building from source

```console
$ go build ./cmd/scry
```

Go 1.27 or newer. Pinned versions and their reasons are recorded in [`docs/versions.md`](docs/versions.md).

## Security promise

- Kubeconfig contents, tokens, server addresses and file paths never appear in output, error messages, or logs; cluster reports identify the cluster by alias only.
- Test fixtures are synthetic (`node-a`, `lab-one`, `example.invalid`); nothing real is committed.
- Vulnerabilities and security questions: see [SECURITY.md](SECURITY.md). Contributing (DCO): see [CONTRIBUTING.md](CONTRIBUTING.md).

## Licence

Apache License 2.0. See [LICENSE](LICENSE).

Copyright 2026 Evan Eugene Pincham.
