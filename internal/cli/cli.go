// Package cli wires the cobra commands. Command bodies for status and wait
// are K1; K0 freezes the flag surface, validation, version output and the
// exit-code mapping.
package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/Hedronite/scry/internal/exitcode"
	"github.com/Hedronite/scry/internal/report"
)

// Streams carries the process streams so tests can capture them.
type Streams struct {
	Out io.Writer
	Err io.Writer
}

// Execute runs scry with args and returns the process exit code. Errors from
// cobra itself (unknown flag, unknown command, bad arg count) are untyped and
// become usage errors (8); every error a command returns must be an
// *exitcode.Error.
func Execute(args []string, s Streams) int {
	root := NewRoot(s)
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	if err == nil {
		return int(exitcode.Green)
	}
	if e, ok := exitcode.Of(err); ok {
		if e.Message != "" {
			_, _ = fmt.Fprintf(s.Err, "scry: %s\n", e.Message)
		}
		if e.Code == exitcode.Usage {
			_, _ = fmt.Fprintln(s.Err, "try 'scry --help'")
		}
		return int(e.Code)
	}
	_, _ = fmt.Fprintf(s.Err, "scry: %s\ntry 'scry --help'\n", err)
	return int(exitcode.Usage)
}

// NewRoot builds the command tree.
func NewRoot(s Streams) *cobra.Command {
	root := &cobra.Command{
		Use:           "scry",
		Short:         rootShort,
		Long:          rootLong,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE:          func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	root.SetOut(s.Out)
	root.SetErr(s.Err)
	root.CompletionOptions.DisableDefaultCmd = true
	root.AddCommand(newStatus(s), newWait(s), newVersion(s))
	return root
}

// Common holds the flags shared by status and wait.
type Common struct {
	Kubeconfig         string
	AllowEnvKubeconfig bool
	Context            string
	Cluster            string
	Expected           string
	Config             string
	JSON               bool
	Quiet              bool
}

func (c *Common) bind(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVar(&c.Kubeconfig, "kubeconfig", "", flagKubeconfig)
	f.BoolVar(&c.AllowEnvKubeconfig, "allow-env-kubeconfig", false, flagAllowEnv)
	f.StringVar(&c.Context, "context", "", flagContext)
	f.StringVar(&c.Cluster, "cluster", "", flagCluster)
	f.StringVar(&c.Expected, "expected", "", flagExpected)
	f.StringVar(&c.Config, "config", "", flagConfig)
	f.BoolVar(&c.JSON, "json", false, flagJSON)
	f.BoolVar(&c.Quiet, "quiet", false, flagQuiet)
}

func usage(format string, args ...any) error {
	return &exitcode.Error{Code: exitcode.Usage, Reason: exitcode.InternalError, Message: fmt.Sprintf(format, args...)}
}

// Validate checks flag combinations that need no I/O. It returns usage
// errors (exit 8).
func (c *Common) Validate() error {
	if c.JSON && c.Quiet {
		return usage("--json and --quiet cannot be used together")
	}
	switch c.Expected {
	case "", string(report.Light), string(report.Dark):
	default:
		return usage("--expected must be \"dark\" or \"light\"")
	}
	if c.Cluster != "" && !report.ValidAlias(c.Cluster) {
		return usage("--cluster must match %s", report.AliasPattern)
	}
	return nil
}
