package vigil_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/Hedronite/scry/internal/exitcode"
	"github.com/Hedronite/scry/internal/report"
	"github.com/Hedronite/scry/internal/schematest"
)

const (
	vendorDir  = "../../testdata/vigil/vendor"
	vendorFile = vendorDir + "/vigil.summary.v0.schema.json"
	goldenDir  = "../../testdata/vigil"
	statusDir  = "../../testdata/status"
	schemaID   = "https://github.com/Hedronite/vigil/schema/vigil.summary.v0.schema.json"
)

func read(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// vigilDef compiles one $defs entry of the vendored Vigil schema.
func vigilDef(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(read(t, vendorFile)))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(schemaID, doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile(schemaID + "#/$defs/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func validate(t *testing.T, s *jsonschema.Schema, raw []byte) error {
	t.Helper()
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return s.Validate(inst)
}

// mapScry is the reference mapping of docs/vigil.md. It is deliberately
// independent of package report: a Vigil reader reads the JSON, not Scry types.
func mapScry(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var d struct {
		GeneratedAt string `json:"generated_at"`
		Cluster     struct {
			Alias string `json:"alias"`
		} `json:"cluster"`
		Readyz *struct {
			OK bool `json:"ok"`
		} `json:"readyz"`
		Nodes *struct {
			Total int `json:"total"`
			Ready int `json:"ready"`
		} `json:"nodes"`
		Observed bool     `json:"observed"`
		Severity *string  `json:"severity"`
		Reasons  []string `json:"reasons"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	reasons := d.Reasons
	if reasons == nil {
		reasons = []string{}
	}
	out := map[string]any{
		"observed":      d.Observed,
		"generated_at":  d.GeneratedAt,
		"cluster_alias": d.Cluster.Alias,
		"reasons":       reasons,
	}
	if !d.Observed {
		out["severity"] = nil
		return out
	}
	out["severity"] = *d.Severity
	var pass, fail, total, ready int
	if d.Readyz != nil {
		if d.Readyz.OK {
			pass = 1
		} else {
			fail = 1
		}
	}
	if d.Nodes != nil {
		total, ready = d.Nodes.Total, d.Nodes.Ready
	}
	out["counts"] = map[string]any{
		"nodes_total": total, "nodes_ready": ready, "readyz_pass": pass, "readyz_fail": fail,
	}
	return out
}

// exportable lists the status fixtures that can reach an --out file: every
// fixture without a wait block (status never carries one).
func exportable(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(statusDir + "/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no status fixtures: %v", err)
	}
	var names []string
	for _, f := range files {
		var d struct {
			Wait json.RawMessage `json:"wait"`
		}
		if err := json.Unmarshal(read(t, f), &d); err != nil {
			t.Fatal(err)
		}
		if len(d.Wait) == 0 {
			names = append(names, strings.TrimSuffix(filepath.Base(f), ".json"))
		}
	}
	return names
}

// TestGoldensValidateAgainstVigilScry: every expected scry{} validates against
// Vigil's frozen $defs/scry, and every input is a valid Scry document.
func TestGoldensValidateAgainstVigilScry(t *testing.T) {
	scry := vigilDef(t, "scry")
	for _, name := range exportable(t) {
		t.Run(name, func(t *testing.T) {
			if err := schematest.Status(read(t, statusDir+"/"+name+".json")); err != nil {
				t.Fatalf("input is not a valid scry.status/v0 document: %v", err)
			}
			if err := validate(t, scry, read(t, goldenDir+"/"+name+".scry.json")); err != nil {
				t.Fatalf("expected scry{} does not validate against the vendored Vigil $defs/scry: %v", err)
			}
		})
	}
	// Vigil's own minimal golden (scry-unobserved) is also accepted, so a reader
	// may emit the short form.
	if err := validate(t, scry, []byte(`{"observed": false}`)); err != nil {
		t.Fatalf("minimal unobserved block rejected: %v", err)
	}
}

// TestVigilScryRejectsWhatItMustNot proves the vendored schema is the strict
// one: if it stopped rejecting these, the golden check above would prove less.
func TestVigilScryRejectsWhatItMustNot(t *testing.T) {
	scry := vigilDef(t, "scry")
	for name, bad := range map[string]string{
		"node names":    `{"observed":true,"nodes":["node-a"]}`,
		"server detail": `{"observed":true,"server":{"version":"v1"}}`,
		"status":        `{"observed":true,"status":"ok"}`,
		"bad severity":  `{"observed":true,"severity":"OK"}`,
		"bad alias":     `{"observed":true,"cluster_alias":"lab.one"}`,
		"bad reason":    `{"observed":true,"reasons":["Not Snake"]}`,
		"extra count":   `{"observed":true,"counts":{"nodes_unknown":1}}`,
		"negative":      `{"observed":true,"counts":{"nodes_total":-1}}`,
	} {
		if err := validate(t, scry, []byte(bad)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// TestMappingMatchesGoldens: the documented mapping, applied to each Scry
// document, produces exactly the golden block.
func TestMappingMatchesGoldens(t *testing.T) {
	for _, name := range exportable(t) {
		t.Run(name, func(t *testing.T) {
			var want map[string]any
			if err := json.Unmarshal(read(t, goldenDir+"/"+name+".scry.json"), &want); err != nil {
				t.Fatal(err)
			}
			got := mapScry(t, read(t, statusDir+"/"+name+".json"))
			// Normalise through JSON so numbers compare as numbers.
			b, _ := json.Marshal(got)
			var gotN map[string]any
			_ = json.Unmarshal(b, &gotN)
			if !reflect.DeepEqual(gotN, want) {
				t.Fatalf("mapping differs\n got: %s\nwant: %s", b, read(t, goldenDir+"/"+name+".scry.json"))
			}
		})
	}
}

// TestEveryExportableFixtureHasAGoldenAndNoOrphans keeps the pairs complete.
func TestEveryExportableFixtureHasAGoldenAndNoOrphans(t *testing.T) {
	want := map[string]bool{}
	for _, n := range exportable(t) {
		want[n+".scry.json"] = true
	}
	files, err := filepath.Glob(goldenDir + "/*.scry.json")
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, f := range files {
		have[filepath.Base(f)] = true
	}
	for n := range want {
		if !have[n] {
			t.Errorf("no golden for status fixture %s", strings.TrimSuffix(n, ".scry.json"))
		}
	}
	for n := range have {
		if !want[n] {
			t.Errorf("golden %s has no exportable status fixture", n)
		}
	}
}

// TestReasonsFitVigilPattern: every reason Scry can emit passes Vigil's reason
// shape, so Vigil can copy reasons as they are.
func TestReasonsFitVigilPattern(t *testing.T) {
	reason := vigilDef(t, "reason")
	reasons := exitcode.All()
	if len(reasons) == 0 {
		t.Fatal("no reasons")
	}
	for _, r := range reasons {
		b, _ := json.Marshal(string(r))
		if err := validate(t, reason, b); err != nil {
			t.Errorf("reason %q does not fit Vigil's reason pattern: %v", r, err)
		}
	}
}

// TestAliasFitsVigilSlug: a valid Scry alias is always a valid Vigil slug, and
// the two patterns agree on a probe set, so cluster_alias is copied unchanged.
func TestAliasFitsVigilSlug(t *testing.T) {
	slug := vigilDef(t, "slug")
	probes := []string{
		"", "a", "0", "lab-one", "a-b-c", "-a", "a-", "A", "a.b", "a_b", "a b", "a/b", "a:b",
		"unnamed", report.UnnamedAlias,
		strings.Repeat("a", 32), strings.Repeat("a", 33), "a" + strings.Repeat("-", 31), "é",
	}
	for _, p := range probes {
		b, _ := json.Marshal(p)
		vigilOK := validate(t, slug, b) == nil
		if got := report.ValidAlias(p); got != vigilOK {
			t.Errorf("alias %q: Scry valid=%v, Vigil slug valid=%v", p, got, vigilOK)
		}
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`).MatchString(report.UnnamedAlias) {
		t.Errorf("fallback alias %q is not a valid alias", report.UnnamedAlias)
	}
}

// TestGeneratedAtFitsVigilTimestamp: Scry's timestamp encoding is accepted by
// Vigil's pattern-pinned timestamp.
func TestGeneratedAtFitsVigilTimestamp(t *testing.T) {
	ts := vigilDef(t, "timestamp")
	for _, name := range exportable(t) {
		var d struct {
			GeneratedAt string `json:"generated_at"`
		}
		if err := json.Unmarshal(read(t, statusDir+"/"+name+".json"), &d); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(d.GeneratedAt)
		if err := validate(t, ts, b); err != nil {
			t.Errorf("%s: generated_at %q: %v", name, d.GeneratedAt, err)
		}
	}
	b, err := json.Marshal(report.Document{GeneratedAt: report.Timestamp{}})
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		GeneratedAt string `json:"generated_at"`
	}
	_ = json.Unmarshal(b, &d)
	enc, _ := json.Marshal(d.GeneratedAt)
	if err := validate(t, ts, enc); err != nil {
		t.Errorf("Scry's own encoding %q: %v", d.GeneratedAt, err)
	}
}

// TestVendoredSchemaIsUntouched: the vendored file is byte-identical to the
// recorded Vigil commit's file (sha256 in SOURCE).
func TestVendoredSchemaIsUntouched(t *testing.T) {
	src := string(read(t, vendorDir+"/SOURCE"))
	sum := sha256.Sum256(read(t, vendorFile))
	if want := "sha256:  " + hex.EncodeToString(sum[:]); !strings.Contains(src, want) {
		t.Fatalf("vendored schema changed: SOURCE does not carry %s", want)
	}
	if !strings.Contains(src, "commit:  44dfb0bb8d5c0598a44175cbe4478953f5c95d91") {
		t.Fatal("SOURCE does not name Vigil commit 44dfb0b")
	}
}
