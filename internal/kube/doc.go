// Package kube is the only place Scry talks to a cluster: the allowlisted
// transport, the kubeconfig loader with its refusals, and the three
// observations. Owned by the backend seat; this file only pins client-go in
// go.mod until the real files land. Delete the blank import when they do.
package kube

import _ "k8s.io/client-go/rest"
