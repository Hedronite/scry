# Scry

Scry is a small, inspect-only command-line tool that answers one question about a Kubernetes cluster: **is it ready, and if not, what is not?**

It is a companion to [hedronetes-h3s](https://github.com/VirtualMachinist/hedronetes-h3s) and works against stock Kubernetes too. It is not an operator or a control plane: it reads and reports, and it is built so it cannot write.

> **Status:** pre-release (contract phase). The output contract — the `scry.status/v0` document, the exit codes, and the request allowlist — is frozen; the commands do not observe a real cluster yet.

## What Scry does

- Reports API server reachability, `/readyz`, and node readiness as a table or as JSON (`scry.status/v0`).
- Waits, with bounded polling, until a cluster is ready (`scry wait --for=readyz`).
- Treats a cluster you have declared *expected dark* as healthy when it is unreachable.

## What Scry does not do

- No create, update, patch, delete, or apply. Every request passes a local allowlist of read-only `GET` paths; anything else is refused before it reaches the network.
- No watches and no long-lived connections: `wait` polls with bounded backoff, one fresh request at a time.
- No in-cluster mode and no implicit kubeconfig: you pass the path explicitly.
- No exec-based or auth-provider credentials, and no skipping TLS verification.

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

`scry wait` refuses to wait on a declared-dark cluster (exit 3, reason `wait_expected_dark`): nothing can make a documented-dark apiserver pass `readyz`. Pass `--expected light` if the cluster is supposed to come up.

## Output

`scry status` prints a table:

```text
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

`--json` prints one `scry.status/v0` document instead (see [`schema/scry.status.v0.json`](schema/scry.status.v0.json)); `--quiet` prints nothing and lets the exit code answer. Scripts and agents should read the JSON form: the table is for people.

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

## What "ready" means

- **API server:** `up` means the peer answered (even with a 401 or a certificate failure — the endpoint exists and speaks), `down` means the TCP connection failed, `unknown` means the connection was accepted and then went silent.
- **Readyz:** on stock Kubernetes, Scry parses `?verbose` sub-checks when the server offers them. On h3s, `/readyz` answers a bare `ok` and nothing else; Scry reports that as `aggregate` and prints "no sub-checks reported" rather than inventing detail. A bare `ok` proves the API server process answers — it does not prove workloads can run.
- **Nodes:** the `Ready` condition of every node in `/api/v1/nodes`, plus `NetworkUnavailable`. All nodes not Ready is RED; some is YELLOW. Scry never dials kubelet ports and never hardcodes component names.

## Configuration file

Scry reads no config file unless you name one with `--config`. The file is TOML (`scry.config/v0`), holds paths and expected state — never credentials — and is validated strictly:

```toml
schema = "scry.config/v0"

[[cluster]]
alias = "lab-one"
kubeconfig = "./lab-one.kubeconfig"
expected = "dark"
```

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
