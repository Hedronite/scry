# Pinned versions

No floating versions: `go.mod` pins every module, CI pins every action by commit SHA. Update a row here in the same change that moves the pin.

| What | Pin | Why |
| --- | --- | --- |
| Go | 1.27.4 | newest stable at skeleton time (2026-11-13); 1.27.3 carries GO-2026-6859 (net/http) |
| `k8s.io/client-go`, `k8s.io/apimachinery` | v0.37.2 | newest stable minor (Kubernetes 1.37) |
| `golang.org/x/net` | v0.62.0 | indirect; v0.57.0 carries HTTP/2 advisories (GO-2026-6603, -6611, -6612, -6617) |
| `golang.org/x/text` | v0.43.0 | indirect; required by x/net v0.62.0 |
| `github.com/spf13/cobra` | v1.10.2 | newest stable |
| `github.com/pelletier/go-toml/v2` | v2.4.3 | `scry.config/v0` parser, strict mode |
| `github.com/santhosh-tekuri/jsonschema/v6` | v6.0.3 | test-only schema validation |
| golangci-lint | v2.15.0 | lint; config in `.golangci.yml` |
| govulncheck | v1.9.0 | vulnerability scan in CI |
| `actions/checkout` | v7.0.1 (`3d3c42e5…`) | |
| `actions/setup-go` | v7.0.0 (`b7ad1dad…`) | |
| `golangci/golangci-lint-action` | v9.3.0 (`ba0d7d2e…`) | |
| `actions/upload-artifact` | v7.0.2 (`cf430e03…`) | the reproducibility job publishes `SHA256SUMS` so a second machine can compare hashes |
| `actions/attest-build-provenance` | v4.2.2 (`4d101475…`) | build provenance for each release archive (`release.yml` only) |

## Release pins

`hack/release.sh` builds with exactly the Go version `go.mod` names (`GOTOOLCHAIN=go<version>`), so moving the Go row above moves every release. It is also the only place the build flags live: `CGO_ENABLED=0`, `-trimpath`, `-buildvcs=true`, `-ldflags "-s -w -buildid= -X .../internal/cli.version=<tag>"`, `GOAMD64=v1`, `GOARM64=v8.0`. The archive writer (`hack/mkarchive`) is standard library only, so a release adds no tool dependency; its output depends on the Go version, which is pinned. Dependabot is declined: pins move by hand, with this file, in the same change.

## Exec-auth refusal is a runtime guarantee

`client-go/rest` imports the exec auth plugin, so exec code is linked into the binary whatever Scry imports. The `depguard` rules in `.golangci.yml` are a tripwire against Scry importing `os/exec` or `client-go/plugin` itself, not proof that exec auth cannot run. The guarantee is runtime: the kubeconfig loader refuses `exec`, `auth-provider` and `insecure-skip-tls-verify` before building a client, and a canary test proves a refused exec command never runs.
