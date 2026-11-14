# Changelog

All notable changes to Scry are recorded here. Scry follows [Semantic Versioning](https://semver.org/); while the major version is 0, the documents `scry.status/v0`, `scry.config/v0` and `scry.version/v0` change only additively.

## [0.1.0] - 2026-11-14

First release. Scry is a small, inspect-only command-line tool that answers one question about a Kubernetes or h3s cluster: is it ready, and if not, what is not?

### Commands

- `scry status` observes a cluster once and prints a table, or with `--json` one `scry.status/v0` document. The exit code is the answer: 0 GREEN, 1 YELLOW, 2 RED, 3 config, 4 auth, 5 TLS, 6 transport, 7 internal, 8 usage.
- `scry wait --for=readyz` polls with bounded backoff (500 ms growing 1.5x to a 5 s cap, no jitter) until the cluster is ready or `--timeout` passes. A timeout exits 2 with reason `timeout`.
- `scry version` prints the version and commit; `--json` prints `scry.version/v0`.
- `--quiet` silences stdout and stderr; only a usage error prints one line.

### Inspect-only by construction

- Every request passes a local allowlist of read-only `GET` paths (`/readyz`, `/livez`, `/version`, `/api/v1/nodes`, `/api/v1/nodes/{name}`) with fixed query keys; anything else is refused before the network. No watches, no long-lived connections, no redirects.
- Credentials are limited to client certificates, static bearer tokens and token files. `exec` and `auth-provider` credentials and `insecure-skip-tls-verify` are refused at load.
- The kubeconfig is never implicit: pass `--kubeconfig`, or use `--config` (`scry.config/v0`, TOML) which holds paths, never credentials. A kubeconfig path, server URL, token or file content never appears in output.

### Readiness model

- The API server result is `up`, `down` or `unknown`; a cluster declared `--expected dark` is GREEN when unreachable.
- Node readiness comes from the `Ready` condition (including `Unknown`) and `NetworkUnavailable`. On h3s the node conditions are the real readiness signal, because `/readyz` answers a hard-coded `ok` before authentication.
- A run that cannot be observed (config, auth, TLS, transport) still emits a valid document with `observed: false` and no severity, so no consumer can mistake a failed look for a RED cluster.

### Vigil export

- `scry status --out PATH` also writes the full `scry.status/v0` document atomically (temp file in the same directory, fsync, rename, directory fsync, mode 0644), on exits 0, 1, 2, 4, 5 and 6, never on 3, 7 or 8. A failed write exits 3 with reason `out_write_failed`; if only the final directory fsync fails after the rename, the file is in place, stderr carries a warning and the exit code follows the status result.
- `docs/vigil.md` maps the document to the `scry{}` block of Vigil's summary, with golden pairs checked against a vendored copy of Vigil's schema. One Scry file feeds Vigil one cluster today.
- systemd and launchd timer examples are in `docs/examples/`.

### Release

- Reproducible, CGO-free builds for linux/amd64, linux/arm64 and darwin/arm64 from `hack/release.sh`: pinned Go, `-trimpath`, empty build id, no build date, deterministic archives, `SHA256SUMS`. CI builds twice in two directories and fails if any hash differs.
- Release archives carry GitHub build provenance attestations; verify with `gh attestation verify`.
- Performance budgets and numbers are in `docs/performance.md`.
