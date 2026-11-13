package cli

import (
	"encoding/json"
	"fmt"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/spf13/cobra"

	"github.com/Hedronite/scry/internal/exitcode"
)

// Set at link time:
//
//	-X github.com/Hedronite/scry/internal/cli.version=1.2.3
//	-X github.com/Hedronite/scry/internal/cli.commit=abc1234
var (
	version = "0.0.0-dev"
	commit  = ""
)

func newStatus(_ Streams) *cobra.Command {
	var c Common
	cmd := &cobra.Command{
		Use:     "status",
		Short:   statusShort,
		Long:    statusLong,
		Example: statusExample,
		Args:    cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if err := c.Validate(); err != nil {
				return err
			}
			return notImplemented("status")
		},
	}
	c.bind(cmd)
	return cmd
}

func newWait(_ Streams) *cobra.Command {
	var (
		c       Common
		forWhat string
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:     "wait",
		Short:   waitShort,
		Long:    waitLong,
		Example: waitExample,
		Args:    cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if err := c.Validate(); err != nil {
				return err
			}
			if forWhat != "readyz" {
				return usage("--for must be \"readyz\"")
			}
			if timeout <= 0 {
				return usage("--timeout must be positive")
			}
			return notImplemented("wait")
		},
	}
	c.bind(cmd)
	cmd.Flags().StringVar(&forWhat, "for", "", flagFor)
	cmd.Flags().DurationVar(&timeout, "timeout", 60*time.Second, flagTimeout)
	return cmd
}

// VersionDoc is scry.version/v0.
type VersionDoc struct {
	Schema  string `json:"schema"`
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Go      string `json:"go"`
}

// CurrentVersion describes this build.
func CurrentVersion() VersionDoc {
	c := commit
	if c == "" {
		c = "unknown"
		if bi, ok := debug.ReadBuildInfo(); ok {
			for _, s := range bi.Settings {
				if s.Key == "vcs.revision" && s.Value != "" {
					c = s.Value
					if len(c) > 12 {
						c = c[:12]
					}
				}
			}
		}
	}
	return VersionDoc{Schema: "scry.version/v0", Version: version, Commit: c, Go: runtime.Version()}
}

func newVersion(s Streams) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "version",
		Short: versionShort,
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			v := CurrentVersion()
			if asJSON {
				b, err := json.Marshal(v)
				if err != nil {
					return exitcode.Wrap(exitcode.InternalError, err, "cannot encode version")
				}
				_, _ = fmt.Fprintf(s.Out, "%s\n", b)
				return nil
			}
			_, _ = fmt.Fprintf(s.Out, "scry %s (%s)\n", v.Version, v.Commit)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, flagVersionJSON)
	return cmd
}
