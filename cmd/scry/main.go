// Command scry reports readiness of a Kubernetes or h3s cluster. It is
// inspect-only: see the allowlist in internal/kube.
package main

import (
	"os"

	"github.com/Hedronite/scry/internal/cli"
)

func main() {
	os.Exit(cli.Execute(os.Args[1:], cli.Streams{Out: os.Stdout, Err: os.Stderr}))
}
