package cli

// Help text. Owned by the frontend seat: rewrite freely, keep the constant
// names (commands.go and cli.go reference them).
const (
	rootShort = "Report whether a Kubernetes or h3s cluster is ready (inspect-only)"
	rootLong  = "Scry reads a cluster and says whether it is ready. It sends only allowlisted GET requests and never changes anything."

	statusShort   = "Observe a cluster once and report readiness"
	statusLong    = "Observe a cluster once and report readiness. (Placeholder: help text lands with the frontend slice.)"
	statusExample = "  scry status --kubeconfig PATH"

	waitShort   = "Poll until the cluster is ready or the timeout passes"
	waitLong    = "Poll until the cluster is ready or the timeout passes. (Placeholder: help text lands with the frontend slice.)"
	waitExample = "  scry wait --for=readyz --kubeconfig PATH --timeout 60s"

	versionShort = "Print the scry version"

	flagKubeconfig = "path to the kubeconfig file (required)"
	flagAllowEnv   = "use $KUBECONFIG when --kubeconfig is not given"
	flagContext    = "kubeconfig context to use (default: current-context)"
	flagCluster    = "cluster alias for the report (a-z, 0-9, '-', up to 32 characters)"
	flagExpected   = "declare the apiserver expected to be \"dark\" or \"light\" (default light)"
	flagConfig     = "path to a scry.config/v0 file"
	flagJSON       = "print the scry.status/v0 document as JSON"
	flagQuiet      = "print nothing; the exit code is the answer"
	flagFor        = "what to wait for; only \"readyz\" is supported"
	flagTimeout    = "give up after this long"

	flagVersionJSON = "print scry.version/v0 as JSON"
)
