// Package config loads scry.config/v0: a TOML file, read only when --config is
// given, that maps cluster aliases to kubeconfig paths and expected state. It
// holds paths, never credentials.
package config

import (
	"bytes"
	"errors"
	"io"
	"os"
	"regexp"
	"slices"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/Hedronite/scry/internal/exitcode"
	"github.com/Hedronite/scry/internal/report"
)

// SchemaV0 is the required value of the top-level schema key.
const SchemaV0 = "scry.config/v0"

const (
	maxFileBytes   = 64 << 10
	defaultTimeout = 5 * time.Second
	minTimeout     = 100 * time.Millisecond
	maxTimeout     = 60 * time.Second
)

// Cluster is one [[cluster]] entry after defaults and validation.
type Cluster struct {
	Alias      string
	Kubeconfig string
	Expected   report.Expected
	Timeout    time.Duration
}

// Config is a validated scry.config/v0 file.
type Config struct {
	Clusters    []Cluster
	Declaration *string // copied into every document; nil when the key is absent
}

type rawFile struct {
	Schema      string       `toml:"schema"`
	Declaration *string      `toml:"declaration"`
	Cluster     []rawCluster `toml:"cluster"`
}

type rawCluster struct {
	Alias      string `toml:"alias"`
	Kubeconfig string `toml:"kubeconfig"`
	Expected   string `toml:"expected"`
	Timeout    string `toml:"timeout"`
}

var declarationRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// The keys of scry.config/v0, in their exact spelling.
var (
	fileKeys    = []string{"schema", "declaration", "cluster"}
	clusterKeys = []string{"alias", "kubeconfig", "expected", "timeout"}
)

// exactKeys reports whether every key is spelled as scry.config/v0 spells it.
// The strict decoder matches struct fields without regard to case, so on its
// own it would accept "Alias", and let it silently override "alias".
func exactKeys(data []byte) bool {
	var doc map[string]any
	if err := toml.Unmarshal(data, &doc); err != nil {
		return false
	}
	for key := range doc {
		if !slices.Contains(fileKeys, key) {
			return false
		}
	}
	clusters, present := doc["cluster"]
	if !present {
		return true
	}
	entries, ok := clusters.([]any)
	if !ok {
		return false
	}
	for _, entry := range entries {
		table, ok := entry.(map[string]any)
		if !ok {
			return false
		}
		for key := range table {
			if !slices.Contains(clusterKeys, key) {
				return false
			}
		}
	}
	return true
}

// Load reads and validates the file at path. Every failure is an
// exitcode.ConfigInvalid error (exit 3) whose message never contains the path
// or file contents.
func Load(path string) (*Config, error) {
	f, err := os.Open(path) // #nosec G304 -- explicit --config path is the point
	if err != nil {
		return nil, exitcode.Wrap(exitcode.ConfigInvalid, err, "cannot read the config file given with --config")
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return nil, exitcode.Wrap(exitcode.ConfigInvalid, err, "cannot read the config file given with --config")
	}
	return Parse(data)
}

// Parse validates config bytes. Unknown keys are rejected.
func Parse(data []byte) (*Config, error) {
	if len(data) > maxFileBytes {
		return nil, exitcode.New(exitcode.ConfigInvalid, "config file is larger than %d bytes", maxFileBytes)
	}
	var raw rawFile
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		var se *toml.StrictMissingError
		if errors.As(err, &se) {
			return nil, exitcode.New(exitcode.ConfigInvalid, "config has an unknown key; see scry.config/v0")
		}
		// toml errors quote file content; keep only the kind of failure.
		return nil, exitcode.Wrap(exitcode.ConfigInvalid, err, "config is not valid TOML")
	}
	if !exactKeys(data) {
		return nil, exitcode.New(exitcode.ConfigInvalid, "config has an unknown key; see scry.config/v0")
	}
	if raw.Schema != SchemaV0 {
		return nil, exitcode.New(exitcode.ConfigInvalid, "config must start with schema = %q", SchemaV0)
	}
	if raw.Declaration != nil && !declarationRE.MatchString(*raw.Declaration) {
		return nil, exitcode.New(exitcode.ConfigInvalid,
			"declaration must be sha256: followed by 64 lowercase hex digits; leave the key out when undeclared")
	}
	seen := map[string]bool{}
	cfg := &Config{Declaration: raw.Declaration}
	for i, rc := range raw.Cluster {
		c, err := validate(i+1, rc)
		if err != nil {
			return nil, err
		}
		if seen[c.Alias] {
			return nil, exitcode.New(exitcode.ConfigInvalid, "cluster #%d repeats alias %q", i+1, c.Alias)
		}
		seen[c.Alias] = true
		cfg.Clusters = append(cfg.Clusters, c)
	}
	return cfg, nil
}

func validate(n int, rc rawCluster) (Cluster, error) {
	c := Cluster{Alias: rc.Alias, Kubeconfig: rc.Kubeconfig, Expected: report.Light, Timeout: defaultTimeout}
	if !report.ValidAlias(rc.Alias) {
		return c, exitcode.New(exitcode.ConfigInvalid, "cluster #%d: alias must match %s", n, report.AliasPattern)
	}
	if rc.Kubeconfig == "" {
		return c, exitcode.New(exitcode.ConfigInvalid, "cluster %q: kubeconfig path is required", rc.Alias)
	}
	switch rc.Expected {
	case "", string(report.Light):
	case string(report.Dark):
		c.Expected = report.Dark
	default:
		return c, exitcode.New(exitcode.ConfigInvalid, "cluster %q: expected must be \"light\" or \"dark\"", rc.Alias)
	}
	if rc.Timeout != "" {
		d, err := time.ParseDuration(rc.Timeout)
		if err != nil || d < minTimeout || d > maxTimeout {
			return c, exitcode.New(exitcode.ConfigInvalid, "cluster %q: timeout must be a duration between %s and %s", rc.Alias, minTimeout, maxTimeout)
		}
		c.Timeout = d
	}
	return c, nil
}

// Lookup returns the cluster with the given alias.
func (c *Config) Lookup(alias string) (Cluster, error) {
	for _, cl := range c.Clusters {
		if cl.Alias == alias {
			return cl, nil
		}
	}
	names := make([]string, 0, len(c.Clusters))
	for _, cl := range c.Clusters {
		names = append(names, cl.Alias)
	}
	e := exitcode.New(exitcode.ConfigInvalid, "no cluster %q in the config; available: %v", alias, names)
	e.Available = names
	return Cluster{}, e
}
