# Scry

Scry is a small, inspect-only command-line tool that answers one question about a Kubernetes cluster: **is it ready, and if not, what is not?**

It is a companion to [hedronetes-h3s](https://github.com/VirtualMachinist/hedronetes-h3s) and works against stock Kubernetes too. It is not an operator or a control plane: it reads and reports, and it is built so it cannot write.

> **Status:** pre-release. The contract (output schema, exit codes, request allowlist) is being frozen now. There is no usable binary yet.

## What Scry does

- Reports API server reachability, `/readyz`, and node readiness as a table or as JSON (`scry.status/v0`).
- Waits, with bounded polling, until a cluster is ready (`scry wait --for=readyz`).
- Treats a cluster you have declared *expected dark* as healthy when it is unreachable.

## What Scry does not do

- No create, update, patch, delete, or apply. Every request passes a local allowlist of read-only `GET` paths; anything else is refused before it reaches the network.
- No watches and no long-lived connections.
- No in-cluster mode and no implicit kubeconfig: you pass the path explicitly.
- No exec-based or auth-provider credentials, and no skipping TLS verification.

## Licence

Apache License 2.0. See [LICENSE](LICENSE).

Copyright 2026 Evan Eugene Pincham.
