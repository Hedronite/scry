package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Hedronite/scry/internal/config"
	"github.com/Hedronite/scry/internal/exitcode"
	"github.com/Hedronite/scry/internal/kube"
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
			enc := json.NewEncoder(s.Out)
			enc.SetIndent("", "  ")
			if err := enc.Encode(d); err != nil {
				return exitcode.Wrap(exitcode.InternalError, err, "cannot encode the document")
			}
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

func runStatus(ctx context.Context, s Streams, c *Common) error {
	if err := c.Validate(); err != nil {
		return err
	}
	opts, err := resolve(c)
	if err != nil {
		return emit(s, c, failureDoc(clusterOf(opts), err), err)
	}
	obs, err := newObserver(opts)
	if err != nil {
		return emit(s, c, failureDoc(clusterOf(opts), err), err)
	}
	return emit(s, c, report.Build(obs.Observe(ctx), nowFunc()), nil)
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
