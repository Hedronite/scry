package report_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Hedronite/scry/internal/exitcode"
	"github.com/Hedronite/scry/internal/report"
	"github.com/Hedronite/scry/internal/schematest"
)

func ptr[T any](v T) *T { return &v }

func ts() report.Timestamp {
	return report.Timestamp{Time: time.Date(2026, 11, 13, 20, 0, 0, 0, time.UTC)}
}

// full is a document with every optional block populated.
func full() report.Document {
	return report.Document{
		Schema:      report.SchemaV0,
		GeneratedAt: ts(),
		Cluster:     report.Cluster{Alias: "lab-one", Expected: report.Light},
		Server:      &report.Server{Version: "v1.34.0+h3s", Flavor: "h3s"},
		Apiserver:   report.Apiserver{Result: report.Up, HTTPStatus: ptr(200), LatencyMS: ptr(int64(12)), Livez: "ok"},
		Readyz: &report.Readyz{Mode: report.ModeVerbose, OK: false, Checks: []report.Check{
			{Name: "etcd", OK: false, Message: "failed"}, {Name: "ping", OK: true},
		}},
		Nodes: &report.Nodes{Total: 3, Ready: 1, Items: []report.Node{
			{Name: "node-a", Ready: true},
			{Name: "node-b", Ready: false, Reason: "RuntimeNotReady", NetworkUnavailable: true},
			{Name: "node-c", Ready: false, Reason: "NodeStatusUnknown", Unknown: true},
		}},
		Observed: true,
		Severity: report.Yellow,
		Reasons:  []exitcode.Reason{exitcode.NodeNotReady},
		Wait:     &report.Wait{Outcome: report.WaitReady, Polls: 3, ElapsedMS: 1800},
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestDriftFullDocumentValidates marshals a fully populated document and
// validates it, so the Go types and the schema cannot drift apart.
func TestDriftFullDocumentValidates(t *testing.T) {
	if err := schematest.Status(mustJSON(t, full())); err != nil {
		t.Fatalf("full document does not validate: %v", err)
	}
}

func TestMinimalUnobservedValidates(t *testing.T) {
	d := report.Document{
		Schema: report.SchemaV0, GeneratedAt: ts(),
		Cluster:   report.Cluster{Alias: "unnamed", Expected: report.Light},
		Apiserver: report.Apiserver{Result: report.Up},
		Observed:  false,
		Error:     &report.ErrorBody{Code: exitcode.AuthRejected, Message: "server rejected the credentials"},
	}
	b := mustJSON(t, d)
	if err := schematest.Status(b); err != nil {
		t.Fatalf("unobserved document does not validate: %v", err)
	}
	if !strings.Contains(string(b), `"reasons":[]`) {
		t.Errorf("nil reasons must encode as []: %s", b)
	}
	if strings.Contains(string(b), `"severity"`) {
		t.Errorf("severity must be absent when unobserved: %s", b)
	}
	if got := d.ExitCode(); got != exitcode.Auth {
		t.Errorf("ExitCode = %d, want %d", got, exitcode.Auth)
	}
}

func TestInvalidDocumentsRejected(t *testing.T) {
	good := mustJSON(t, full())
	mutate := func(f func(m map[string]any)) []byte {
		var m map[string]any
		if err := json.Unmarshal(good, &m); err != nil {
			t.Fatal(err)
		}
		f(m)
		return mustJSON(t, m)
	}
	cases := map[string][]byte{
		"unknown top-level field":   mutate(func(m map[string]any) { m["extra"] = 1 }),
		"observed without severity": mutate(func(m map[string]any) { delete(m, "severity") }),
		"unobserved with severity": mutate(func(m map[string]any) {
			m["observed"] = false
			m["error"] = map[string]any{"code": "internal", "message": "x"}
		}),
		"unobserved without error": mutate(func(m map[string]any) { m["observed"] = false; delete(m, "severity") }),
		"observed with error": mutate(func(m map[string]any) {
			m["error"] = map[string]any{"code": "internal", "message": "x"}
		}),
		"alias is a url":   mutate(func(m map[string]any) { m["cluster"].(map[string]any)["alias"] = "https://x.example" }),
		"alias too long":   mutate(func(m map[string]any) { m["cluster"].(map[string]any)["alias"] = strings.Repeat("a", 33) }),
		"bad severity":     mutate(func(m map[string]any) { m["severity"] = "ORANGE" }),
		"bad reason":       mutate(func(m map[string]any) { m["reasons"] = []string{"made_up"} }),
		"bad result":       mutate(func(m map[string]any) { m["apiserver"].(map[string]any)["result"] = "maybe" }),
		"bad schema const": mutate(func(m map[string]any) { m["schema"] = "scry.status/v1" }),
		"non-utc time":     mutate(func(m map[string]any) { m["generated_at"] = "2026-11-13T15:00:00-05:00" }),
		"node reason over 120": mutate(func(m map[string]any) {
			m["nodes"].(map[string]any)["items"].([]any)[0].(map[string]any)["reason"] = strings.Repeat("r", 121)
		}),
		"nested unknown field": mutate(func(m map[string]any) { m["apiserver"].(map[string]any)["url"] = "x" }),
	}
	for name, b := range cases {
		if err := schematest.Status(b); err == nil {
			t.Errorf("%s: accepted, want rejection", name)
		}
	}
}

// TestReasonEnumMatchesExitcode keeps the schema enum and exitcode.All() equal.
func TestReasonEnumMatchesExitcode(t *testing.T) {
	enum, err := schematest.StatusReasons()
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, r := range exitcode.All() {
		want = append(want, string(r))
	}
	if !slices.Equal(enum, want) {
		t.Fatalf("schema reasons %v\n != exitcode.All() %v", enum, want)
	}
}

func TestExitCodeMapping(t *testing.T) {
	obs := func(s report.Severity) report.Document { return report.Document{Observed: true, Severity: s} }
	cases := []struct {
		name string
		doc  report.Document
		want exitcode.Code
	}{
		{"green", obs(report.Green), exitcode.Green},
		{"yellow", obs(report.Yellow), exitcode.Yellow},
		{"red", obs(report.Red), exitcode.Red},
		{"tls", report.Document{Error: &report.ErrorBody{Code: exitcode.TLSVerifyFailed}}, exitcode.TLS},
		{"silent peer", report.Document{Error: &report.ErrorBody{Code: exitcode.HTTPUnresponsive}}, exitcode.Transport},
		{"wait on dark", report.Document{Error: &report.ErrorBody{Code: exitcode.WaitExpectedDark}}, exitcode.Config},
		{"no error body", report.Document{}, exitcode.Internal},
		{"wait timeout beats last poll", report.Document{Observed: true, Severity: report.Yellow,
			Wait: &report.Wait{Outcome: report.WaitTimeout, Polls: 4}}, exitcode.Red},
		{"wait timeout, never observed", report.Document{Error: &report.ErrorBody{Code: exitcode.HTTPUnresponsive},
			Wait: &report.Wait{Outcome: report.WaitTimeout, Polls: 4}}, exitcode.Red},
	}
	for _, c := range cases {
		if got := c.doc.ExitCode(); got != c.want {
			t.Errorf("%s: ExitCode = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestAlias(t *testing.T) {
	for _, ok := range []string{"a", "lab-one", "0abc", strings.Repeat("a", 32)} {
		if !report.ValidAlias(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "-a", "A", "a_b", "a.b", "a@b", "a b", strings.Repeat("a", 33), "https://h"} {
		if report.ValidAlias(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
	if got := report.ResolveAlias("", "ops@example.org", "ctx-one", "later"); got != "ctx-one" {
		t.Errorf("ResolveAlias = %q, want ctx-one", got)
	}
	if got := report.ResolveAlias("user@host.example"); got != report.UnnamedAlias {
		t.Errorf("ResolveAlias = %q, want unnamed", got)
	}
}

// TestGoldenFixtures validates every file under testdata/status (must pass)
// and testdata/status-invalid (must fail). Fixtures are synthetic.
func TestGoldenFixtures(t *testing.T) {
	check := func(dir string, wantValid bool) {
		files, err := filepath.Glob(filepath.Join("..", "..", "testdata", dir, "*.json"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			err = schematest.Status(b)
			if wantValid && err != nil {
				t.Errorf("%s: %v", f, err)
			}
			if !wantValid && err == nil {
				t.Errorf("%s: accepted, want rejection", f)
			}
		}
	}
	check("status", true)
	check("status-invalid", false)
}
