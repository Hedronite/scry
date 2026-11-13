# Contributing to Scry

Scry is an inspect-only readiness CLI. A change that lets it write to a cluster, hold a watch open, run another program, or read a credential it was not handed will not be merged, whatever else it does. Read `docs/versions.md` for the pins and the exec-auth note before touching `internal/kube`.

## Workflow

- Open a pull request against `main`. `main` is protected: no direct pushes, linear history, squash merge.
- CI must be green: build, `go vet`, `go test -race`, golangci-lint, govulncheck, schema conformance, `hack/scrub.sh`, and the three cross-compiles.
- Tests use `httptest` and throwaway kubeconfigs created in `t.TempDir()`. Do not commit a kubeconfig, certificate, token, address or hostname, real or not; fixtures use synthetic values and slugs.
- Commit and PR text describe the change. Do not name hosts, machines or people.

## Developer Certificate of Origin

By contributing you certify the [Developer Certificate of Origin 1.1](https://developercertificate.org/). Sign every commit:

```
git commit -s
```

which adds `Signed-off-by: Your Name <you@example.org>`.

## Contract changes

`scry.status/v0` is frozen: additive changes only, each paired with an update to `schema/scry.status.v0.json` and a golden fixture. A breaking change needs a new major (`v1`) and a migration note.
