// Package report holds the scry.status/v0 document and its pure renderers.
//
// It must not import client-go or anything that pulls it in: the table
// renderer is a pure function of Document. internal/kube produces an
// Observation; Build (build.go) turns it into a Document; table.go renders it.
package report

import (
	"encoding/json"
	"regexp"
	"time"

	"github.com/Hedronite/scry/internal/exitcode"
)

// SchemaV0 is the value of Document.Schema.
const SchemaV0 = "scry.status/v0"

// MaxReasonLen bounds node condition reasons copied from the server.
const MaxReasonLen = 120

// MaxMessageLen bounds readyz check messages copied from the server.
const MaxMessageLen = 200

// Severity is the shared GREEN/YELLOW/RED vocabulary.
type Severity string

const (
	Green  Severity = "GREEN"
	Yellow Severity = "YELLOW"
	Red    Severity = "RED"
)

// Expected is the declared state of the apiserver.
type Expected string

const (
	Light Expected = "light"
	Dark  Expected = "dark"
)

// Result is what the wire said about the apiserver (spec section 2).
type Result string

const (
	Up      Result = "up"      // peer answered anything, even 401/500 or a cert failure
	Down    Result = "down"    // TCP connect failed
	Unknown Result = "unknown" // connected, then silent past the deadline
)

// ReadyzMode records how much detail the server gave.
type ReadyzMode string

const (
	ModeVerbose   ReadyzMode = "verbose"
	ModeAggregate ReadyzMode = "aggregate" // bare "ok": no sub-checks reported
)

// Wait outcomes.
const (
	WaitReady   = "ready"
	WaitTimeout = "timeout"
)

// Timestamp marshals as UTC RFC 3339 with second precision.
type Timestamp struct{ time.Time }

const timestampLayout = "2006-01-02T15:04:05Z"

// MarshalJSON implements json.Marshaler.
func (t Timestamp) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.UTC().Format(timestampLayout))
}

// UnmarshalJSON implements json.Unmarshaler.
func (t *Timestamp) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.Parse(timestampLayout, s)
	if err != nil {
		return err
	}
	t.Time = v
	return nil
}

// Cluster identifies the cluster by alias only, never a URL or path.
type Cluster struct {
	Alias    string   `json:"alias"`
	Expected Expected `json:"expected"`
}

// Server comes from GET /version.
type Server struct {
	Version string `json:"version"`
	Flavor  string `json:"flavor"` // "h3s" when gitVersion ends +h3s, else "kubernetes"
}

// Apiserver is the L4/L7 verdict. Livez is set only when /readyz failed.
type Apiserver struct {
	Result     Result `json:"result"`
	HTTPStatus *int   `json:"http_status,omitempty"`
	LatencyMS  *int64 `json:"latency_ms,omitempty"`
	Livez      string `json:"livez,omitempty"` // "ok" | "failed"
}

// Check is one readyz sub-check.
type Check struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
}

// Readyz is the parsed /readyz response.
type Readyz struct {
	Mode   ReadyzMode `json:"mode"`
	OK     bool       `json:"ok"`
	Checks []Check    `json:"checks"`
}

// Node is one node's readiness.
type Node struct {
	Name               string `json:"name"`
	Ready              bool   `json:"ready"`
	Reason             string `json:"reason,omitempty"`
	NetworkUnavailable bool   `json:"network_unavailable,omitempty"`
}

// Nodes summarises LIST /api/v1/nodes.
type Nodes struct {
	Total int    `json:"total"`
	Ready int    `json:"ready"`
	Items []Node `json:"items"`
}

// ErrorBody explains an unobserved document. Message is printable-safe.
type ErrorBody struct {
	Code    exitcode.Reason `json:"code"`
	Message string          `json:"message"`
}

// Wait is present only on the final document of scry wait.
type Wait struct {
	Outcome   string `json:"outcome"` // WaitReady | WaitTimeout
	Polls     int    `json:"polls"`
	ElapsedMS int64  `json:"elapsed_ms"`
}

// Document is scry.status/v0. Severity is empty when Observed is false.
type Document struct {
	Schema      string            `json:"schema"`
	GeneratedAt Timestamp         `json:"generated_at"`
	Cluster     Cluster           `json:"cluster"`
	Server      *Server           `json:"server,omitempty"`
	Apiserver   Apiserver         `json:"apiserver"`
	Readyz      *Readyz           `json:"readyz,omitempty"`
	Nodes       *Nodes            `json:"nodes,omitempty"`
	Observed    bool              `json:"observed"`
	Severity    Severity          `json:"severity,omitempty"`
	Reasons     []exitcode.Reason `json:"reasons"`
	Error       *ErrorBody        `json:"error,omitempty"`
	Wait        *Wait             `json:"wait,omitempty"`
}

// MarshalJSON guarantees reasons, checks and items encode as [] not null.
func (d Document) MarshalJSON() ([]byte, error) {
	type plain Document
	p := plain(d)
	if p.Reasons == nil {
		p.Reasons = []exitcode.Reason{}
	}
	if p.Readyz != nil && p.Readyz.Checks == nil {
		r := *p.Readyz
		r.Checks = []Check{}
		p.Readyz = &r
	}
	if p.Nodes != nil && p.Nodes.Items == nil {
		n := *p.Nodes
		n.Items = []Node{}
		p.Nodes = &n
	}
	return json.Marshal(p)
}

// ExitCode is the process exit code for the document (spec section 4.3).
// A wait that timed out is always 2, whatever the last poll saw.
func (d Document) ExitCode() exitcode.Code {
	if d.Wait != nil && d.Wait.Outcome == WaitTimeout {
		return exitcode.Red
	}
	if !d.Observed {
		if d.Error != nil {
			if c, ok := exitcode.ForReason(d.Error.Code); ok {
				return c
			}
		}
		return exitcode.Internal
	}
	switch d.Severity {
	case Green:
		return exitcode.Green
	case Yellow:
		return exitcode.Yellow
	case Red:
		return exitcode.Red
	}
	return exitcode.Internal
}

// Observation is what internal/kube hands to Build: raw evidence, no verdict.
// Failure is set when a signal could not be gathered (auth, TLS, transport,
// config); Build then emits an unobserved document and keeps the partial
// evidence in the other fields.
type Observation struct {
	Cluster   Cluster
	Server    *Server
	Apiserver Apiserver
	Readyz    *Readyz
	Nodes     *Nodes
	Failure   *exitcode.Error
}

// AliasPattern is the only shape an alias may take.
const AliasPattern = `^[a-z0-9][a-z0-9-]{0,31}$`

var aliasRE = regexp.MustCompile(AliasPattern)

// UnnamedAlias is used when no candidate is a valid alias.
const UnnamedAlias = "unnamed"

// ValidAlias reports whether s may be used as an alias.
func ValidAlias(s string) bool { return aliasRE.MatchString(s) }

// ResolveAlias returns the first valid candidate (order: --cluster, config
// alias, kubeconfig context name), else "unnamed". Context names often embed
// hosts or emails, so an invalid one is dropped, never sanitised.
func ResolveAlias(candidates ...string) string {
	for _, c := range candidates {
		if ValidAlias(c) {
			return c
		}
	}
	return UnnamedAlias
}
