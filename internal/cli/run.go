package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Hedronite/scry/internal/config"
	"github.com/Hedronite/scry/internal/exitcode"
	"github.com/Hedronite/scry/internal/kube"
	"github.com/Hedronite/scry/internal/outfile"
	"github.com/Hedronite/scry/internal/report"
	"github.com/Hedronite/scry/internal/wait"
)

// Observer is one cluster Scry can observe. *kube.Client implements it.
type Observer interface {
	wait.Observer
	Cluster() report.Cluster
}

// Test seams. Production values are set here; tests swap them.
var (
	newObserver            = func(o kube.Options) (Observer, error) { return kube.New(o) }
	nowFunc                = time.Now
	clock       wait.Clock = wait.RealClock{}
)

// resolve turns flags (and --config) into kube.Options. Flags beat a config
// entry. Failures are exit-3 *exitcode.Error values.
func resolve(c *Common) (kube.Options, error) {
	o := kube.Options{
		Kubeconfig: c.Kubeconfig,
		AllowEnv:   c.AllowEnvKubeconfig,
		Context:    c.Context,
		Cluster:    c.Cluster,
		Expected:   report.Expected(c.Expected),
	}
	if c.Config == "" {
		return o, nil
	}
	cfg, err := config.Load(c.Config)
	if err != nil {
		return o, err
	}
	alias := c.Cluster
	if alias == "" {
		switch len(cfg.Clusters) {
		case 0:
			return o, exitcode.New(exitcode.ConfigInvalid, "the config file lists no clusters")
		case 1:
			alias = cfg.Clusters[0].Alias
		default:
			names := make([]string, 0, len(cfg.Clusters))
			for _, cl := range cfg.Clusters {
				names = append(names, cl.Alias)
			}
			e := exitcode.New(exitcode.ConfigInvalid, "the config lists several clusters; choose one with --cluster")
			e.Available = names
			return o, e
		}
	}
	entry, err := cfg.Lookup(alias)
	if err != nil {
		return o, err
	}
	o.Cluster = entry.Alias
	o.Timeout = entry.Timeout
	if o.Kubeconfig == "" {
		o.Kubeconfig = entry.Kubeconfig
	}
	if o.Expected == "" {
		o.Expected = entry.Expected
	}
	return o, nil
}

// clusterOf is the cluster identity for a document built before a client
// exists (a config or kubeconfig failure).
func clusterOf(o kube.Options) report.Cluster {
	exp := report.Light
	if o.Expected == report.Dark {
		exp = report.Dark
	}
	return report.Cluster{Alias: report.ResolveAlias(o.Cluster), Expected: exp}
}

// failureDoc is the document for a failure that happened before any request:
// unobserved, apiserver result unknown (no verdict from the wire).
func failureDoc(cl report.Cluster, err error) report.Document {
	e, ok := exitcode.Of(err)
	if !ok {
		e = exitcode.Wrap(exitcode.InternalError, err, "unexpected internal error")
	}
	return report.Build(report.Observation{
		Cluster:   cl,
		Apiserver: report.Apiserver{Result: report.Unknown},
		Failure:   e,
	}, nowFunc())
}

// emit prints the document in the chosen form and returns the process exit as
// an error the root command maps to a code without printing anything more.
func emit(s Streams, c *Common, d report.Document, extra error) error {
	if !c.Quiet {
		switch {
		case c.JSON:
			b, err := marshalDoc(d)
			if err != nil {
				return exitcode.Wrap(exitcode.InternalError, err, "cannot encode the document")
			}
			_, _ = s.Out.Write(b)
		default:
			_, _ = fmt.Fprintln(s.Out, report.Render(d))
		}
		if e, ok := exitcode.Of(extra); ok && len(e.Available) > 0 {
			_, _ = fmt.Fprintf(s.Err, "available: %s\n", strings.Join(e.Available, ", "))
		}
	}
	if code := d.ExitCode(); code != exitcode.Green {
		return &exitcode.Error{Code: code}
	}
	return nil
}

// marshalDoc is the one encoding of a document: what --json prints and what
// --out writes, byte for byte.
func marshalDoc(d report.Document) ([]byte, error) {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// outOpt is the status --out flag. set distinguishes an absent flag from an
// empty value.
type outOpt struct {
	set  bool
	path string
}

// validate checks --out before any I/O.
func (o outOpt) validate(c *Common) error {
	if !o.set {
		return nil
	}
	switch o.path {
	case "":
		return usage("--out needs a file path")
	case "-":
		return usage("--out takes a file path, not stdout; use --json to print the document")
	}
	for _, other := range []string{c.Kubeconfig, c.Config} {
		if other != "" && samePath(o.path, other) {
			return usage("--out must not be the kubeconfig or config file")
		}
	}
	return nil
}

// samePath reports whether a and b name the same file, existing or not, so an
// atomic rewrite can never replace a credential or config file.
func samePath(a, b string) bool {
	if fa, err := os.Stat(a); err == nil {
		if fb, err := os.Stat(b); err == nil && os.SameFile(fa, fb) {
			return true
		}
	}
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	return errA == nil && errB == nil && aa == bb
}

// publishable reports whether a document goes to the --out file: exits 0, 1, 2
// (observed) and 4, 5, 6 (unobserved). Exits 3, 7 and 8 write nothing, so the
// last file stays and its reader ages it into stale.
func publishable(d report.Document) bool {
	switch d.ExitCode() {
	case exitcode.Green, exitcode.Yellow, exitcode.Red, exitcode.Auth, exitcode.TLS, exitcode.Transport:
		return true
	case exitcode.Config, exitcode.Internal, exitcode.Usage:
		return false
	}
	return false
}

// finishStatus writes the --out file when the document qualifies, then emits.
// A failed write replaces the document with an unobserved out_write_failed one
// (exit 3) so stdout and the exit code still agree; the old file is untouched.
func finishStatus(s Streams, c *Common, o outOpt, d report.Document, extra error) error {
	if o.set && publishable(d) {
		b, err := marshalDoc(d)
		if err == nil {
			err = outfile.Write(o.path, b)
		}
		if err != nil {
			e := exitcode.Wrap(exitcode.OutWriteFailed, err, "cannot write the --out file")
			if !c.Quiet {
				_, _ = fmt.Fprintf(s.Err, "scry: %s\n", e.Message)
			}
			d, extra = failureDoc(d.Cluster, e), e
		}
	}
	return emit(s, c, d, extra)
}

func runStatus(ctx context.Context, s Streams, c *Common, o outOpt) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if err := o.validate(c); err != nil {
		return err
	}
	opts, err := resolve(c)
	if err != nil {
		return finishStatus(s, c, o, failureDoc(clusterOf(opts), err), err)
	}
	if o.set && opts.Kubeconfig != "" && samePath(o.path, opts.Kubeconfig) {
		return usage("--out must not be the kubeconfig or config file")
	}
	obs, err := newObserver(opts)
	if err != nil {
		return finishStatus(s, c, o, failureDoc(clusterOf(opts), err), err)
	}
	return finishStatus(s, c, o, report.Build(obs.Observe(ctx), nowFunc()), nil)
}

func runWait(ctx context.Context, s Streams, c *Common, forWhat string, timeout time.Duration) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if forWhat != "readyz" {
		return usage("--for must be \"readyz\"")
	}
	if timeout <= 0 {
		return usage("--timeout must be positive")
	}
	opts, err := resolve(c)
	if err != nil {
		return emit(s, c, failureDoc(clusterOf(opts), err), err)
	}
	if opts.Expected == report.Dark {
		return emit(s, c, wait.Refusal(clusterOf(opts), nowFunc()), nil)
	}
	obs, err := newObserver(opts)
	if err != nil {
		return emit(s, c, failureDoc(clusterOf(opts), err), err)
	}
	return emit(s, c, wait.Run(ctx, timeout, obs, clock), nil)
}
