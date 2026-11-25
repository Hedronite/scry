package cli

import (
	"context"
	"encoding/json"
	"errors"
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
	writeOut               = outfile.Write
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
	c.declaration = cfg.Declaration
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

// stamp sets the header fields that the run, not the observation, decides.
func (c *Common) stamp(d report.Document) report.Document {
	d.ValidFor = c.ValidFor
	d.Declaration = c.declaration
	return d
}

// emit prints the document in the chosen form and returns the process exit as
// an error the root command maps to a code without printing anything more.
func emit(s Streams, c *Common, d report.Document, extra error) error {
	d = c.stamp(d)
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
	protected := []string{c.Kubeconfig, c.Config}
	if c.AllowEnvKubeconfig {
		// Every file $KUBECONFIG names, even when --kubeconfig takes over: the
		// run was given permission to read them, never to replace them.
		protected = append(protected, kube.EnvKubeconfigs()...)
	}
	return o.guard(protected...)
}

// guard refuses an --out that names any of the protected files.
func (o outOpt) guard(protected ...string) error {
	if !o.set {
		return nil
	}
	for _, other := range protected {
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
	aa, errA := canonical(a)
	bb, errB := canonical(b)
	return errA == nil && errB == nil && aa == bb
}

// maxLinks bounds the symbolic links canonical follows, as the kernel does.
const maxLinks = 40

// canonical is the absolute name path leads to, following symbolic links even
// when the final target does not exist. It never cleans a path lexically before
// the links in it are resolved: "link/.." is not the directory holding link.
func canonical(path string) (string, error) {
	for range maxLinks {
		target, err := os.Readlink(path)
		if err != nil {
			break // not a link, or nothing there
		}
		if !filepath.IsAbs(target) {
			dir, _ := filepath.Split(path)
			target = dir + target
		}
		path = target
	}
	dir, base := filepath.Split(path)
	if !filepath.IsAbs(dir) {
		// Joined by hand: the working directory may itself be reached through
		// a link, and must be resolved with the rest.
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		dir = wd + string(os.PathSeparator) + dir
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		path = filepath.Join(resolved, base)
	}
	return filepath.Abs(path)
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
	d = c.stamp(d)
	if o.set && publishable(d) {
		b, err := marshalDoc(d)
		if err == nil {
			err = writeOut(o.path, b)
		}
		if errors.Is(err, outfile.ErrDirSync) {
			// The rename succeeded, so the new file is in place and the exit
			// code stays what the status result says. Only crash durability of
			// the rename is unconfirmed: say so, never fail on it.
			if !c.Quiet {
				_, _ = fmt.Fprintln(s.Err, "scry: warning: the --out file was written but the directory sync failed")
			}
			err = nil
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
	// The config entry may have supplied the kubeconfig.
	if err := o.guard(opts.Kubeconfig); err != nil {
		return err
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
