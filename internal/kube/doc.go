// Package kube is the only place Scry talks to a cluster. It holds three
// things and nothing else:
//
//   - transport.go: the allowlisted RoundTripper. Every client in this process
//     is built from a rest.Config carrying it, so typed calls, raw /readyz
//     requests and every redirect hop are checked before a byte leaves the
//     process.
//   - config.go: the kubeconfig loader and its refusals. exec auth,
//     auth-provider plugins and insecure TLS are refused at load, because
//     client-go/rest links the exec plugin whether or not we import it.
//   - observe.go: the three readiness signals (GET /readyz?verbose,
//     GET /version, LIST /api/v1/nodes) as raw evidence. No verdict: severity
//     belongs to report.Build.
//
// The package never dials a kubelet, never watches, and never writes.
package kube
