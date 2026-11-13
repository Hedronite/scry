package cli

// Help text. Owned by the frontend seat: rewrite freely, keep the constant
// names (commands.go and cli.go reference them).
const (
	rootShort = "Report whether a Kubernetes or h3s cluster is ready (inspect-only)"

	rootLong = `Scry reads one Kubernetes cluster and answers a single question: is it ready, and if not, what is not?

It sends only allowlisted read-only GET requests (/readyz, /livez, /version, nodes) and is built so it cannot change anything. It never reads an implicit kubeconfig: pass --kubeconfig, or a config entry with --config. Output is a table by default, the scry.status/v0 JSON document with --json, or nothing at all with --quiet (the exit code is the answer; --quiet silences stdout and stderr entirely, and only usage errors print one line).

Exit codes: 0 GREEN, 1 YELLOW, 2 RED (or a wait timeout), 3 config, 4 auth, 5 TLS, 6 transport, 7 internal, 8 usage. See 'scry status --help' for the cluster-side meanings.`

	statusShort = "Observe a cluster once and report readiness"

	statusLong = `Observe the cluster once and report readiness: API server reachability, /readyz (verbose when the server offers it), and node readiness from the Ready conditions of /api/v1/nodes.

The apiserver verdict is:
  up        the peer answered, even with 401, 500, or a certificate failure
  down      the TCP connection failed
  unknown   no verdict from the wire: connected, then silent past the
            deadline, or the run failed before any request (config problem,
            refused credentials, TLS failure); the exit code says which

A cluster declared --expected dark is GREEN (exit 0) when unreachable: dark on purpose is not a failure. If a declared-dark cluster answers, its readiness is reported normally. Readyz detail depends on the server: h3s serves a bare "ok" (reported as aggregate, with no sub-checks); stock Kubernetes serves ?verbose sub-checks. A node whose Ready condition is neither True nor False is shown as UNKNOWN and counts as not ready. On h3s, node conditions are the real readiness signal; /readyz only proves the API server answers.

Cluster selection: --config with --cluster picks that cluster's entry (kubeconfig path, expected state, timeout); with one entry in the file, --cluster can be omitted; flags always beat the entry. --cluster without --config only overrides the report's alias.

Output: table by default, scry.status/v0 JSON with --json, nothing with --quiet. Exit codes for an observed cluster: 0 GREEN (including expected dark), 1 YELLOW, 2 RED. Without a document: 3 config, 4 auth, 5 TLS, 6 transport, 7 internal.`

	statusExample = `  # report on a cluster (table)
  scry status --kubeconfig ./lab-one.kubeconfig

  # machine-readable scry.status/v0
  scry status --kubeconfig ./lab-one.kubeconfig --json

  # use a saved cluster entry: kubeconfig, expected state, timeout
  scry status --config ./scry.toml --cluster lab-one

  # a cluster that is intentionally unreachable: GREEN, exit 0
  scry status --kubeconfig ./lab-one.kubeconfig --expected dark`

	waitShort = "Poll until the cluster is ready or the timeout passes"

	waitLong = `Poll the cluster until it is ready. Every poll observes the cluster once (fresh allowlisted requests, connection closed after each, so rotated certificates are picked up), on a bounded schedule: 500ms growing 1.5x to a 5s cap, no jitter, until a poll reports ready (observed, /readyz ok, apiserver up) or --timeout (default 60s) expires.

Ready exits 0; ready but degraded exits 1. A timeout keeps the last poll's document, adds reason "timeout", and always exits 2. Config (3), auth (4), TLS (5) and internal (7) failures fail fast: waiting cannot heal them. Unreachable (down) and silent peers keep polling until the timeout.

If the cluster is declared --expected dark, wait refuses immediately: exit 3, reason wait_expected_dark, zero requests made. Nothing can make a documented-dark apiserver pass readyz; pass --expected light to wait on a cluster that is supposed to come up.

There are no watches and no long-lived connections. An interrupt (Ctrl-C) ends the process by default: no cleanup, no summary document.`

	waitExample = `  # wait up to 2 minutes for the cluster to come up
  scry wait --for=readyz --kubeconfig ./lab-one.kubeconfig --timeout 2m

  # wait on a saved entry; the entry's timeout applies unless overridden
  scry wait --config ./scry.toml --cluster lab-one

  # gate a script on readiness without printing anything
  scry wait --for=readyz --kubeconfig ./lab-one.kubeconfig --quiet`

	versionShort = "Print the scry version"

	flagKubeconfig = "path to the kubeconfig file (required unless --config supplies it)"
	flagAllowEnv   = "use $KUBECONFIG when --kubeconfig is not given"
	flagContext    = "kubeconfig context to use (default: current-context)"
	flagCluster    = "cluster alias for the report (a-z, 0-9, '-', up to 32 characters); with --config selects that cluster's entry, without it overrides the alias"
	flagExpected   = "declare the apiserver expected to be \"dark\" or \"light\" (default light; beats the config entry)"
	flagConfig     = "path to a scry.config/v0 file; with --cluster selects that cluster's entry"
	flagJSON       = "print the scry.status/v0 document as JSON"
	flagQuiet      = "print nothing; the exit code is the answer (silences stdout and stderr; usage errors still print one line)"
	flagFor        = "what to wait for; only \"readyz\" is supported"
	flagTimeout    = "give up after this long (default 60s; beats the timeout in the config entry)"

	flagVersionJSON = "print scry.version/v0 as JSON"
)
