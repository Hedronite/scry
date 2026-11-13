package cli

// Help text. Owned by the frontend seat: rewrite freely, keep the constant
// names (commands.go and cli.go reference them).
const (
	rootShort = "Report whether a Kubernetes or h3s cluster is ready (inspect-only)"

	rootLong = `Scry reads one Kubernetes cluster and answers a single question: is it ready, and if not, what is not?

It sends only allowlisted read-only GET requests (/readyz, /livez, /version, nodes) and is built so it cannot change anything. It never reads an implicit kubeconfig: pass --kubeconfig. Output is a table by default, the scry.status/v0 JSON document with --json, or nothing at all with --quiet (the exit code is the answer).

Exit codes: 0 GREEN, 1 YELLOW, 2 RED (or a wait timeout), 3 config, 4 auth, 5 TLS, 6 transport, 7 internal, 8 usage. See 'scry status --help' for the cluster-side meanings.`

	statusShort = "Observe a cluster once and report readiness"

	statusLong = `Observe the cluster once and report readiness: API server reachability, /readyz (verbose when the server offers it), and node readiness from the Ready conditions of /api/v1/nodes.

The apiserver verdict is:
  up        the peer answered, even with 401, 500, or a certificate failure
  down      the TCP connection failed
  unknown   the connection was accepted, then went silent past the deadline

A cluster declared --expected dark is GREEN (exit 0) when unreachable: dark on purpose is not a failure. If a declared-dark cluster answers, its readiness is reported normally. Readyz detail depends on the server: h3s serves a bare "ok" (reported as aggregate, with no sub-checks); stock Kubernetes serves ?verbose sub-checks. On h3s, node conditions are the real readiness signal; /readyz only proves the API server answers.

Output: table by default, scry.status/v0 JSON with --json, nothing with --quiet. Exit codes for an observed cluster: 0 GREEN (including expected dark), 1 YELLOW, 2 RED. Without a document: 3 config, 4 auth, 5 TLS, 6 transport, 7 internal.`

	statusExample = `  # report on a cluster (table)
  scry status --kubeconfig ./lab-one.kubeconfig

  # machine-readable scry.status/v0
  scry status --kubeconfig ./lab-one.kubeconfig --json

  # a cluster that is intentionally unreachable: GREEN, exit 0
  scry status --kubeconfig ./lab-one.kubeconfig --expected dark

  # resolve credentials from $KUBECONFIG instead of a path
  scry status --allow-env-kubeconfig --json`

	waitShort = "Poll until the cluster is ready or the timeout passes"

	waitLong = `Poll the cluster with bounded backoff (500ms growing 1.5x to a 5s cap) until /readyz passes or --timeout (default 60s) expires. Every poll is a fresh allowlisted read-only request; there are no watches and no long-lived connections.

On success the exit code follows the last poll's severity: 0, or 1 when ready but degraded. A timeout is always 2, with reason "timeout". Config (3), auth (4) and TLS (5) failures fail fast: waiting cannot heal them.

If the cluster is declared --expected dark, wait refuses immediately (exit 3, reason wait_expected_dark): nothing can make a documented-dark apiserver pass readyz. Pass --expected light to wait on a cluster that is supposed to come up.`

	waitExample = `  # wait up to 2 minutes for readyz
  scry wait --for=readyz --kubeconfig ./lab-one.kubeconfig --timeout 2m

  # wait on a cluster that is supposed to come up
  scry wait --for=readyz --kubeconfig ./lab-one.kubeconfig --expected light

  # gate a script on readiness without printing anything
  scry wait --for=readyz --kubeconfig ./lab-one.kubeconfig --quiet`

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
	flagTimeout    = "give up after this long (default 60s)"

	flagVersionJSON = "print scry.version/v0 as JSON"
)
