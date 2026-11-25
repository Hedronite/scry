package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hedronite/scry/internal/exitcode"
	"github.com/Hedronite/scry/internal/report"
	"github.com/Hedronite/scry/internal/schematest"
)

var digest = "sha256:" + strings.Repeat("a", 64)

func declared(t *testing.T) string {
	t.Helper()
	return writeConfig(t, "schema = \"scry.config/v0\"\ndeclaration = \""+digest+"\"\n"+
		"[[cluster]]\nalias = \"solo\"\nkubeconfig = \"/synthetic/solo\"\n"+
		"[[cluster]]\nalias = \"dim\"\nkubeconfig = \"/synthetic/dim\"\nexpected = \"dark\"\n")
}

// Every path that emits a document carries the header, in stdout and in --out.
func TestEveryDocumentCarriesTheHeader(t *testing.T) {
	fail := func(r exitcode.Reason) func(*testing.T) {
		return func(t *testing.T) { install(t, okObs(1, 1)).newErr = exitcode.New(r, "synthetic") }
	}
	ok := func(o report.Observation) func(*testing.T) {
		return func(t *testing.T) { install(t, o) }
	}
	cfg := declared(t)
	absent := filepath.Join(t.TempDir(), "absent.toml")
	missingDir := filepath.Join(t.TempDir(), "absent", "status.json")
	solo := []string{"--config", cfg, "--cluster", "solo"}
	with := func(base []string, more ...string) []string { return append(append([]string{}, base...), more...) }
	cases := []struct {
		name        string
		prep        func(*testing.T)
		args        []string
		code        int
		declaration string
	}{
		{"status green", ok(okObs(1, 1)), with([]string{"status"}, solo...), 0, digest},
		{"status red", ok(readyzFailing()), []string{"status", "--kubeconfig", "synthetic"}, 2, ""},
		{"status auth failure", fail(exitcode.AuthRejected), with([]string{"status"}, solo...), 4, digest},
		{"status kubeconfig failure", fail(exitcode.KubeconfigMissing), with([]string{"status"}, solo...), 3, digest},
		{"status expected dark", ok(darkObs()), []string{"status", "--config", cfg, "--cluster", "dim"}, 0, digest},
		{"status unknown cluster", ok(okObs(1, 1)), []string{"status", "--config", cfg, "--cluster", "nope"}, 3, digest},
		{"status no config file", ok(okObs(1, 1)), []string{"status", "--config", absent}, 3, ""},
		{"status out write failure", ok(okObs(1, 1)), with(with([]string{"status"}, solo...), "--out", missingDir), 3, digest},
		{"wait ready", ok(okObs(1, 1)), with([]string{"wait", "--for", "readyz"}, solo...), 0, digest},
		{"wait timeout", ok(readyzFailing()), with([]string{"wait", "--for", "readyz", "--timeout", "2s"}, solo...), 2, digest},
		{"wait refusal", ok(okObs(1, 1)), []string{"wait", "--for", "readyz", "--config", cfg, "--cluster", "dim"}, 3, digest},
		{"wait fail fast", fail(exitcode.TLSVerifyFailed), []string{"wait", "--for", "readyz", "--kubeconfig", "synthetic"}, 5, ""},
		{"wait unknown cluster", ok(okObs(1, 1)), []string{"wait", "--for", "readyz", "--config", cfg, "--cluster", "nope"}, 3, digest},
	}
	for _, c := range cases {
		for _, validFor := range []string{"", "7"} {
			t.Run(c.name+"/"+validFor, func(t *testing.T) {
				c.prep(t)
				args, want := with(c.args, "--json"), `"valid_for": 120,`
				if validFor != "" {
					args, want = with(args, "--valid-for", validFor), `"valid_for": 7,`
				}
				code, out, errs := run(t, args...)
				if code != c.code {
					t.Fatalf("code=%d want %d err=%q", code, c.code, errs)
				}
				if err := schematest.Status([]byte(out)); err != nil {
					t.Fatalf("%v\n%s", err, out)
				}
				dec := `"declaration": null,`
				if c.declaration != "" {
					dec = `"declaration": "` + c.declaration + `",`
				}
				for _, s := range []string{want, dec, `"observed": `} {
					if !strings.Contains(out, s) {
						t.Errorf("missing %s in %s", s, out)
					}
				}
				if strings.Contains(out, `"observed": false`) && strings.Contains(out, `"severity"`) {
					t.Errorf("unobserved with a severity: %s", out)
				}
			})
		}
	}
}

func TestOutFileCarriesTheHeader(t *testing.T) {
	install(t, okObs(1, 1))
	_, path := seed(t)
	code, _, errs := run(t, "status", "--config", declared(t), "--cluster", "solo", "--quiet", "--valid-for", "300", "--out", path)
	got := fileOf(t, path)
	if code != 0 || !strings.Contains(got, `"valid_for": 300,`) || !strings.Contains(got, `"declaration": "`+digest+`",`) {
		t.Fatalf("code=%d err=%q file=%s", code, errs, got)
	}
}

func TestValidForIsAWholeNumberOfSeconds(t *testing.T) {
	for _, verb := range [][]string{{"status"}, {"wait", "--for", "readyz"}} {
		for _, bad := range []string{"0", "-1", "60s", "1.5", ""} {
			s := install(t, okObs(1, 1))
			code, out, errs := run(t, append(append([]string{}, verb...), "--kubeconfig", "synthetic", "--json", "--valid-for="+bad)...)
			if code != 8 || out != "" || s.created != 0 || !strings.Contains(errs, "valid-for") {
				t.Errorf("%v --valid-for=%q: code=%d out=%q err=%q", verb, bad, code, out, errs)
			}
		}
	}
}
