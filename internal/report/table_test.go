package report_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Hedronite/scry/internal/exitcode"
	"github.com/Hedronite/scry/internal/report"
)

func fxPtr[T any](v T) *T { return &v }

func fxTS() report.Timestamp {
	return report.Timestamp{Time: time.Date(2026, 11, 13, 20, 0, 0, 0, time.UTC)}
}

// labDocuments mirrors the testdata/status fixtures with Go structs, so the
// renderer and the fixtures cannot drift apart. Golden expectations are exact.
func docGreen() report.Document {
	return report.Document{
		Schema:      report.SchemaV0,
		GeneratedAt: fxTS(),
		Cluster:     report.Cluster{Alias: "lab-one", Expected: report.Light},
		Server:      &report.Server{Version: "v1.34.0+h3s", Flavor: "h3s"},
		Apiserver:   report.Apiserver{Result: report.Up, HTTPStatus: fxPtr(200), LatencyMS: fxPtr(int64(12))},
		Readyz: &report.Readyz{Mode: report.ModeVerbose, OK: true, Checks: []report.Check{
			{Name: "etcd", OK: true}, {Name: "ping", OK: true},
		}},
		Nodes: &report.Nodes{Total: 2, Ready: 2, Items: []report.Node{
			{Name: "node-a", Ready: true},
			{Name: "node-b", Ready: true},
		}},
		Observed: true,
		Severity: report.Green,
		Reasons:  []exitcode.Reason{},
	}
}

func docYellow() report.Document {
	return report.Document{
		Schema:      report.SchemaV0,
		GeneratedAt: fxTS(),
		Cluster:     report.Cluster{Alias: "lab-one", Expected: report.Light},
		Server:      &report.Server{Version: "v1.34.0+h3s", Flavor: "h3s"},
		Apiserver:   report.Apiserver{Result: report.Up, HTTPStatus: fxPtr(200), LatencyMS: fxPtr(int64(18))},
		Readyz: &report.Readyz{Mode: report.ModeVerbose, OK: true, Checks: []report.Check{
			{Name: "etcd", OK: true}, {Name: "ping", OK: true},
		}},
		Nodes: &report.Nodes{Total: 2, Ready: 1, Items: []report.Node{
			{Name: "node-a", Ready: true},
			{Name: "node-b", Ready: false, Reason: "RuntimeNotReady", NetworkUnavailable: true},
		}},
		Observed: true,
		Severity: report.Yellow,
		Reasons: []exitcode.Reason{
			exitcode.NodeNotReady, exitcode.NetworkUnavailable,
		},
	}
}

func docRed() report.Document {
	return report.Document{
		Schema:      report.SchemaV0,
		GeneratedAt: fxTS(),
		Cluster:     report.Cluster{Alias: "lab-one", Expected: report.Light},
		Server:      &report.Server{Version: "v1.34.0+h3s", Flavor: "h3s"},
		Apiserver:   report.Apiserver{Result: report.Up, HTTPStatus: fxPtr(200), LatencyMS: fxPtr(int64(9))},
		Readyz:      &report.Readyz{Mode: report.ModeAggregate, OK: false, Checks: []report.Check{}},
		Nodes:       &report.Nodes{Total: 0, Ready: 0, Items: []report.Node{}},
		Observed:    true,
		Severity:    report.Red,
		Reasons:     []exitcode.Reason{exitcode.ReadyzFailed, exitcode.NoNodes},
	}
}

func docExpectedDark() report.Document {
	return report.Document{
		Schema:      report.SchemaV0,
		GeneratedAt: fxTS(),
		Cluster:     report.Cluster{Alias: "lab-one", Expected: report.Dark},
		Apiserver:   report.Apiserver{Result: report.Down},
		Observed:    true,
		Severity:    report.Green,
		Reasons:     []exitcode.Reason{exitcode.ExpectedDark},
	}
}

func docUnobservedAuth() report.Document {
	return report.Document{
		Schema:      report.SchemaV0,
		GeneratedAt: fxTS(),
		Cluster:     report.Cluster{Alias: "lab-two", Expected: report.Light},
		Apiserver:   report.Apiserver{Result: report.Up, HTTPStatus: fxPtr(401)},
		Observed:    false,
		Reasons:     []exitcode.Reason{exitcode.AuthRejected},
		Error:       &report.ErrorBody{Code: exitcode.AuthRejected, Message: "server rejected the credentials"},
	}
}

func docUnobservedTLS() report.Document {
	return report.Document{
		Schema:      report.SchemaV0,
		GeneratedAt: fxTS(),
		Cluster:     report.Cluster{Alias: "lab-two", Expected: report.Light},
		Apiserver:   report.Apiserver{Result: report.Up},
		Observed:    false,
		Reasons:     []exitcode.Reason{exitcode.TLSVerifyFailed},
		Error:       &report.ErrorBody{Code: exitcode.TLSVerifyFailed, Message: "the certificate failed verification"},
	}
}

// docYellowUnknown mirrors yellow-unknown.json: readyz ok, one node whose
// Ready condition has no verdict (Ready=Unknown).
func docYellowUnknown() report.Document {
	return report.Document{
		Schema:      report.SchemaV0,
		GeneratedAt: fxTS(),
		Cluster:     report.Cluster{Alias: "lab-one", Expected: report.Light},
		Server:      &report.Server{Version: "v1.34.0+h3s", Flavor: "h3s"},
		Apiserver:   report.Apiserver{Result: report.Up, HTTPStatus: fxPtr(200), LatencyMS: fxPtr(int64(18))},
		Readyz: &report.Readyz{Mode: report.ModeVerbose, OK: true, Checks: []report.Check{
			{Name: "etcd", OK: true}, {Name: "ping", OK: true},
		}},
		Nodes: &report.Nodes{Total: 2, Ready: 1, Items: []report.Node{
			{Name: "node-a", Ready: true},
			{Name: "node-b", Ready: false, Reason: "KubeletNotReady", Unknown: true},
		}},
		Observed: true,
		Severity: report.Yellow,
		Reasons:  []exitcode.Reason{exitcode.NodeNotReady},
	}
}

// docRedAllUnknown mirrors red-all-unknown.json: every node has no verdict,
// so nothing is ready and the cluster is RED.
func docRedAllUnknown() report.Document {
	return report.Document{
		Schema:      report.SchemaV0,
		GeneratedAt: fxTS(),
		Cluster:     report.Cluster{Alias: "lab-one", Expected: report.Light},
		Server:      &report.Server{Version: "v1.34.0+h3s", Flavor: "h3s"},
		Apiserver:   report.Apiserver{Result: report.Up, HTTPStatus: fxPtr(200), LatencyMS: fxPtr(int64(9))},
		Readyz: &report.Readyz{Mode: report.ModeVerbose, OK: true, Checks: []report.Check{
			{Name: "etcd", OK: true}, {Name: "ping", OK: true},
		}},
		Nodes: &report.Nodes{Total: 2, Ready: 0, Items: []report.Node{
			{Name: "node-a", Ready: false, Reason: "KubeletNotReady", Unknown: true},
			{Name: "node-b", Ready: false, Unknown: true},
		}},
		Observed: true,
		Severity: report.Red,
		Reasons:  []exitcode.Reason{exitcode.NodeNotReady},
	}
}

// docWaitReady mirrors wait-ready.json: the final poll of a successful wait,
// GREEN, with the wait block summarising the run.
func docWaitReady() report.Document {
	return report.Document{
		Schema:      report.SchemaV0,
		GeneratedAt: fxTS(),
		Cluster:     report.Cluster{Alias: "lab-one", Expected: report.Light},
		Server:      &report.Server{Version: "v1.34.0+h3s", Flavor: "h3s"},
		Apiserver:   report.Apiserver{Result: report.Up, HTTPStatus: fxPtr(200), LatencyMS: fxPtr(int64(12))},
		Readyz: &report.Readyz{Mode: report.ModeVerbose, OK: true, Checks: []report.Check{
			{Name: "etcd", OK: true}, {Name: "ping", OK: true},
		}},
		Nodes:    &report.Nodes{Total: 1, Ready: 1, Items: []report.Node{{Name: "node-a", Ready: true}}},
		Observed: true,
		Severity: report.Green,
		Reasons:  []exitcode.Reason{},
		Wait:     &report.Wait{Outcome: report.WaitReady, Polls: 3, ElapsedMS: 1800},
	}
}

func docWaitTimeout() report.Document {
	return report.Document{
		Schema:      report.SchemaV0,
		GeneratedAt: fxTS(),
		Cluster:     report.Cluster{Alias: "lab-one", Expected: report.Light},
		Server:      &report.Server{Version: "v1.34.0+h3s", Flavor: "h3s"},
		Apiserver:   report.Apiserver{Result: report.Up, HTTPStatus: fxPtr(200), LatencyMS: fxPtr(int64(12))},
		Readyz:      &report.Readyz{Mode: report.ModeAggregate, OK: false, Checks: []report.Check{}},
		Nodes:       &report.Nodes{Total: 1, Ready: 1, Items: []report.Node{{Name: "node-a", Ready: true}}},
		Observed:    true,
		Severity:    report.Red,
		Reasons:     []exitcode.Reason{exitcode.Timeout, exitcode.ReadyzFailed},
		Wait:        &report.Wait{Outcome: report.WaitTimeout, Polls: 5, ElapsedMS: 60000},
	}
}

// TestRenderGolden pins exact table output for the core document states. The
// examples in README.md are generated from these cases; keep them in step.
func TestRenderGolden(t *testing.T) {
	cases := []struct {
		name string
		doc  report.Document
		want string
	}{
		{
			"green", docGreen(),
			`cluster:    lab-one (expected light)
as of:      2026-11-13T20:00:00Z
server:     v1.34.0+h3s (h3s)
apiserver:  up, http 200, 12ms
readyz:     verbose, ok
            ok    etcd
            ok    ping
nodes:      2 of 2 ready
            ready     node-a
            ready     node-b
severity:   GREEN`,
		},
		{
			"yellow", docYellow(),
			`cluster:    lab-one (expected light)
as of:      2026-11-13T20:00:00Z
server:     v1.34.0+h3s (h3s)
apiserver:  up, http 200, 18ms
readyz:     verbose, ok
            ok    etcd
            ok    ping
nodes:      1 of 2 ready
            ready     node-a
            NOT READY node-b (RuntimeNotReady, network unavailable)
severity:   YELLOW
reasons:    node_not_ready, network_unavailable`,
		},
		{
			"red", docRed(),
			`cluster:    lab-one (expected light)
as of:      2026-11-13T20:00:00Z
server:     v1.34.0+h3s (h3s)
apiserver:  up, http 200, 9ms
readyz:     aggregate, FAILED (no sub-checks reported)
nodes:      no nodes reported
severity:   RED
reasons:    readyz_failed, no_nodes`,
		},
		{
			"expected dark", docExpectedDark(),
			`cluster:    lab-one (expected dark)
as of:      2026-11-13T20:00:00Z
apiserver:  down, expected dark
severity:   GREEN
reasons:    expected_dark`,
		},
		{
			"unobserved auth", docUnobservedAuth(),
			`cluster:    lab-two (expected light)
as of:      2026-11-13T20:00:00Z
apiserver:  up, http 401
error:      auth_rejected: server rejected the credentials
reasons:    auth_rejected`,
		},
		{
			"unobserved tls", docUnobservedTLS(),
			`cluster:    lab-two (expected light)
as of:      2026-11-13T20:00:00Z
apiserver:  up
error:      tls_verify_failed: the certificate failed verification
reasons:    tls_verify_failed`,
		},
		{
			"wait timeout", docWaitTimeout(),
			`cluster:    lab-one (expected light)
as of:      2026-11-13T20:00:00Z
server:     v1.34.0+h3s (h3s)
apiserver:  up, http 200, 12ms
readyz:     aggregate, FAILED (no sub-checks reported)
nodes:      1 of 1 ready
            ready     node-a
severity:   RED
reasons:    timeout, readyz_failed
wait:       timed out after 5 polls, 60.0s`,
		},
		{
			"yellow unknown", docYellowUnknown(),
			`cluster:    lab-one (expected light)
as of:      2026-11-13T20:00:00Z
server:     v1.34.0+h3s (h3s)
apiserver:  up, http 200, 18ms
readyz:     verbose, ok
            ok    etcd
            ok    ping
nodes:      1 of 2 ready
            ready     node-a
            UNKNOWN   node-b (KubeletNotReady)
severity:   YELLOW
reasons:    node_not_ready`,
		},
		{
			"red all unknown", docRedAllUnknown(),
			`cluster:    lab-one (expected light)
as of:      2026-11-13T20:00:00Z
server:     v1.34.0+h3s (h3s)
apiserver:  up, http 200, 9ms
readyz:     verbose, ok
            ok    etcd
            ok    ping
nodes:      0 of 2 ready
            UNKNOWN   node-a (KubeletNotReady)
            UNKNOWN   node-b
severity:   RED
reasons:    node_not_ready`,
		},
		{
			"wait ready", docWaitReady(),
			`cluster:    lab-one (expected light)
as of:      2026-11-13T20:00:00Z
server:     v1.34.0+h3s (h3s)
apiserver:  up, http 200, 12ms
readyz:     verbose, ok
            ok    etcd
            ok    ping
nodes:      1 of 1 ready
            ready     node-a
severity:   GREEN
wait:       ready after 3 polls, 1.8s`,
		},
		{
			"minimal unobserved, no error body",
			report.Document{
				Schema:      report.SchemaV0,
				GeneratedAt: fxTS(),
				Cluster:     report.Cluster{Alias: "unnamed", Expected: report.Light},
				Apiserver:   report.Apiserver{Result: report.Down},
			},
			`cluster:    unnamed (expected light)
as of:      2026-11-13T20:00:00Z
apiserver:  down
error:      (no error body)`,
		},
	}
	for _, c := range cases {
		if got := report.Render(c.doc); got != c.want {
			t.Errorf("%s:\n got:\n%s\nwant:\n%s", c.name, got, c.want)
		}
	}
}

// TestRenderDecoration covers the conditional rows: livez diagnostics, an
// apiserver verdict with no extras, a ready-but-partitioned node, and the
// "ready" wait outcome.
func TestRenderDecoration(t *testing.T) {
	d := docRed()
	d.Apiserver.Livez = "ok"
	if got := report.Render(d); !strings.Contains(got, "livez ok") {
		t.Errorf("livez diagnostic missing:\n%s", got)
	}
	d2 := docGreen()
	d2.Server = nil
	d2.Apiserver = report.Apiserver{Result: report.Up}
	want := "apiserver:  up\n"
	if got := report.Render(d2); !strings.Contains(got, want) {
		t.Errorf("got:\n%s\nwant row: %s", got, want)
	}
	d3 := docGreen()
	d3.Nodes.Items = []report.Node{{Name: "node-a", Ready: true, NetworkUnavailable: true}}
	if got := report.Render(d3); !strings.Contains(got, "ready     node-a (network unavailable)") {
		t.Errorf("network unavailable annotation missing:\n%s", got)
	}
	d3.Nodes.Items = []report.Node{{Name: "node-a", Ready: false, Unknown: true,
		Reason: "KubeletNotReady", NetworkUnavailable: true}}
	if got := report.Render(d3); !strings.Contains(got, "UNKNOWN   node-a (KubeletNotReady, network unavailable)") {
		t.Errorf("unknown node annotation missing:\n%s", got)
	}
	d4 := docWaitTimeout()
	d4.Wait = &report.Wait{Outcome: report.WaitReady, Polls: 3, ElapsedMS: 1800}
	if got := report.Render(d4); !strings.Contains(got, "wait:       ready after 3 polls, 1.8s") {
		t.Errorf("ready wait row missing:\n%s", got)
	}
}

// TestRenderFixtures renders every testdata/status fixture and expects the
// marker that names its state; testdata/status-invalid fixtures must render
// too (the renderer is total, the schema rejects them).
func TestRenderFixtures(t *testing.T) {
	markers := map[string]string{
		"green.json":           "nodes:      2 of 2 ready",
		"yellow.json":          "NOT READY node-b (RuntimeNotReady, network unavailable)",
		"red.json":             "nodes:      no nodes reported",
		"expected-dark.json":   "apiserver:  down, expected dark",
		"unobserved-auth.json": "error:      auth_rejected: server rejected the credentials",
		"unobserved-tls.json":  "error:      tls_verify_failed",
		"wait-timeout.json":    "wait:       timed out after 5 polls, 60.0s",
		"yellow-unknown.json":  "UNKNOWN   node-b (KubeletNotReady)",
		"red-all-unknown.json": "nodes:      0 of 2 ready",
		"wait-ready.json":      "wait:       ready after 3 polls, 1.8s",
	}
	render := func(path string) (string, error) {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		var d report.Document
		if err := json.Unmarshal(b, &d); err != nil {
			return "", err
		}
		return report.Render(d), nil
	}
	for name, marker := range markers {
		got, err := render(filepath.Join("..", "..", "testdata", "status", name))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !strings.Contains(got, marker) {
			t.Errorf("%s: missing %q:\n%s", name, marker, got)
		}
	}
	invalid, err := filepath.Glob(filepath.Join("..", "..", "testdata", "status-invalid", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(invalid) == 0 {
		t.Fatal("no testdata/status-invalid fixtures found")
	}
	for _, f := range invalid {
		got, err := render(f)
		if err != nil {
			t.Errorf("%s: %v", f, err)
		}
		if got == "" {
			t.Errorf("%s: rendered empty", f)
		}
	}
}

// TestRenderNoAddressLeak keeps the renderer honest with the scrub rule: the
// table shows aliases and node names, never a URL or address.
func TestRenderNoAddressLeak(t *testing.T) {
	for _, doc := range []report.Document{docGreen(), docYellow(), docRed(),
		docExpectedDark(), docUnobservedAuth(), docUnobservedTLS(), docWaitTimeout()} {
		got := report.Render(doc)
		for _, banned := range []string{"https://", "http://", "example.invalid", "192.0.2.", "198.51.100.", "203.0.113."} {
			if strings.Contains(got, banned) {
				t.Errorf("table contains %q:\n%s", banned, got)
			}
		}
	}
}
