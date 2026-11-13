package cli

import (
	"bytes"
	"encoding/json"
	"regexp"
	"testing"

	"github.com/Hedronite/scry/internal/exitcode"
	"github.com/Hedronite/scry/internal/schematest"
)

func run(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var o, e bytes.Buffer
	code = Execute(args, Streams{Out: &o, Err: &e})
	return code, o.String(), e.String()
}

func TestVersionPlain(t *testing.T) {
	code, out, _ := run(t, "version")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !regexp.MustCompile(`^scry \S+ \(\S+\)\n$`).MatchString(out) {
		t.Fatalf("out = %q", out)
	}
}

func TestVersionJSONMatchesSchema(t *testing.T) {
	code, out, _ := run(t, "version", "--json")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if err := schematest.Version([]byte(out)); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var v VersionDoc
	if err := json.Unmarshal([]byte(out), &v); err != nil || v.Schema != "scry.version/v0" {
		t.Fatalf("%v %+v", err, v)
	}
}

func TestUsageErrorsExitEightWithoutDocument(t *testing.T) {
	cases := [][]string{
		{"bogus"},
		{"status", "--nope"},
		{"status", "--json", "--quiet"},
		{"status", "--expected", "maybe"},
		{"status", "--cluster", "Bad_Alias"},
		{"wait", "--for", "ready"},
		{"wait"},
		{"wait", "--for", "readyz", "--timeout", "0s"},
		{"status", "extra-arg"},
	}
	for _, args := range cases {
		code, out, errs := run(t, args...)
		if code != int(exitcode.Usage) {
			t.Errorf("%v: code = %d, want 8 (stderr %q)", args, code, errs)
		}
		if out != "" {
			t.Errorf("%v: usage error wrote a document: %q", args, out)
		}
	}
}

// TestFlagSurfaceFrozen pins the K0 flag names (spec section 4.1).
func TestFlagSurfaceFrozen(t *testing.T) {
	root := NewRoot(Streams{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}})
	want := map[string][]string{
		"status":  {"kubeconfig", "allow-env-kubeconfig", "context", "cluster", "expected", "config", "json", "quiet"},
		"wait":    {"kubeconfig", "allow-env-kubeconfig", "context", "cluster", "expected", "config", "json", "quiet", "for", "timeout"},
		"version": {"json"},
	}
	for name, flags := range want {
		cmd, _, err := root.Find([]string{name})
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range flags {
			if cmd.Flags().Lookup(f) == nil {
				t.Errorf("%s: missing --%s", name, f)
			}
		}
	}
	for _, banned := range []string{"insecure-skip-tls-verify", "insecure", "token", "server", "namespace", "watch"} {
		for name := range want {
			cmd, _, _ := root.Find([]string{name})
			if cmd.Flags().Lookup(banned) != nil {
				t.Errorf("%s: banned flag --%s exists", name, banned)
			}
		}
	}
}
