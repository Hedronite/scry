package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hedronite/scry/internal/exitcode"
	"github.com/Hedronite/scry/internal/outfile"
	"github.com/Hedronite/scry/internal/report"
	"github.com/Hedronite/scry/internal/schematest"
)

const sentinel = "previous document\n"

func seed(t *testing.T) (dir, path string) {
	t.Helper()
	dir = t.TempDir()
	path = filepath.Join(dir, "status.json")
	if err := os.WriteFile(path, []byte(sentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, path
}

func fileOf(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func leftovers(t *testing.T, dir string, want ...string) {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range es {
		got = append(got, e.Name())
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("directory holds %v, want %v", got, want)
	}
}

// R4: the file is written on 0, 1, 2 (observed) and 4, 5, 6 (unobserved), and
// holds exactly what --json prints.
func TestOutWrittenOnExit012456(t *testing.T) {
	cases := []struct {
		name string
		code int
		prep func(t *testing.T) *seam
	}{
		{"green", 0, func(t *testing.T) *seam { return install(t, okObs(2, 2)) }},
		{"yellow", 1, func(t *testing.T) *seam { return install(t, okObs(1, 2)) }},
		{"red", 2, func(t *testing.T) *seam { return install(t, readyzFailing()) }},
		{"expected-dark", 0, func(t *testing.T) *seam { return install(t, darkObs()) }},
		{"auth", 4, func(t *testing.T) *seam {
			s := install(t, okObs(1, 1))
			s.newErr = exitcode.New(exitcode.AuthRejected, "the server rejected the credentials")
			return s
		}},
		{"tls", 5, func(t *testing.T) *seam {
			s := install(t, okObs(1, 1))
			s.newErr = exitcode.New(exitcode.TLSVerifyFailed, "the server certificate could not be verified")
			return s
		}},
		{"transport", 6, func(t *testing.T) *seam {
			s := install(t, okObs(1, 1))
			s.newErr = exitcode.New(exitcode.HTTPUnresponsive, "the apiserver connected but gave no usable answer")
			return s
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.prep(t)
			dir, path := seed(t)
			args := []string{"status", "--kubeconfig", "synthetic", "--out", path}
			if c.name == "expected-dark" {
				args = append(args, "--expected", "dark")
			}
			code, _, errs := run(t, append(args, "--json")...)
			if code != c.code || errs != "" {
				t.Fatalf("code=%d err=%q", code, errs)
			}
			// Same encoding as stdout: run again to capture it.
			c.prep(t)
			_, out, _ := run(t, append(args[:len(args):len(args)], "--json")...)
			if got := fileOf(t, path); got != out {
				t.Fatalf("file differs from --json stdout:\nfile: %s\nout:  %s", got, out)
			}
			if err := schematest.Status([]byte(fileOf(t, path))); err != nil {
				t.Fatalf("file is not a valid document: %v", err)
			}
			if c.code >= 4 && strings.Contains(fileOf(t, path), `"severity"`) {
				t.Fatalf("severity present in an unobserved file: %s", fileOf(t, path))
			}
			leftovers(t, dir, "status.json")
		})
	}
}

// R2: the file is the full JSON document whatever stdout shows.
func TestOutHoldsJSONWhenStdoutIsATable(t *testing.T) {
	install(t, okObs(1, 2))
	_, path := seed(t)
	code, out, _ := run(t, "status", "--kubeconfig", "synthetic", "--out", path)
	if code != 1 || !strings.Contains(out, "YELLOW") || strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Fatalf("stdout is not the table: code=%d %q", code, out)
	}
	if err := schematest.Status([]byte(fileOf(t, path))); err != nil {
		t.Fatalf("file is not a valid document: %v", err)
	}
}

func TestQuietStillWritesTheFileAndStaysSilent(t *testing.T) {
	install(t, okObs(2, 2))
	_, path := seed(t)
	code, out, errs := run(t, "status", "--kubeconfig", "synthetic", "--quiet", "--out", path)
	if code != 0 || out != "" || errs != "" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errs)
	}
	if err := schematest.Status([]byte(fileOf(t, path))); err != nil {
		t.Fatalf("file is not a valid document: %v", err)
	}
}

// R4: nothing is written on 3 and 7; the previous file stays byte for byte.
func TestOutUntouchedOnExit3And7(t *testing.T) {
	cases := []struct {
		name   string
		reason exitcode.Reason
		code   int
	}{
		{"config", exitcode.ConfigInvalid, 3},
		{"kubeconfig", exitcode.KubeconfigMissing, 3},
		{"exec-refused", exitcode.ExecAuthRefused, 3},
		{"internal", exitcode.InternalError, 7},
		{"allowlist", exitcode.AllowlistDenied, 7},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := install(t, okObs(1, 1))
			s.newErr = exitcode.New(c.reason, "refused")
			dir, path := seed(t)
			code, _, _ := run(t, "status", "--kubeconfig", "synthetic", "--json", "--out", path)
			if code != c.code {
				t.Fatalf("code = %d, want %d", code, c.code)
			}
			if got := fileOf(t, path); got != sentinel {
				t.Fatalf("file changed: %q", got)
			}
			leftovers(t, dir, "status.json")
		})
	}
	t.Run("no file created", func(t *testing.T) {
		s := install(t, okObs(1, 1))
		s.newErr = exitcode.New(exitcode.KubeconfigMissing, "no kubeconfig")
		dir := t.TempDir()
		code, _, _ := run(t, "status", "--kubeconfig", "synthetic", "--out", filepath.Join(dir, "status.json"))
		if code != 3 {
			t.Fatalf("code = %d", code)
		}
		leftovers(t, dir)
	})
}

// R5: a failed write is exit 3 out_write_failed, an unobserved document on
// stdout, a one-line stderr message without the path, and the old file stays.
func TestOutWriteFailureIsExit3AndLeavesTheOldFile(t *testing.T) {
	install(t, okObs(2, 2))
	dir, path := seed(t)
	bad := filepath.Join(dir, "absent", "status.json")
	code, out, errs := run(t, "status", "--kubeconfig", "synthetic", "--json", "--out", bad)
	if code != 3 {
		t.Fatalf("code = %d", code)
	}
	if err := schematest.Status([]byte(out)); err != nil {
		t.Fatalf("stdout is not a valid document: %v\n%s", err, out)
	}
	for _, want := range []string{`"observed": false`, `"out_write_failed"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %s", want, out)
		}
	}
	if strings.Contains(out, `"severity"`) {
		t.Errorf("severity present on an unobserved document: %s", out)
	}
	if errs != "scry: cannot write the --out file\n" {
		t.Errorf("stderr = %q", errs)
	}
	for _, s := range []string{out, errs} {
		if strings.Contains(s, dir) {
			t.Errorf("output carries the path: %q", s)
		}
	}
	if got := fileOf(t, path); got != sentinel {
		t.Errorf("old file changed: %q", got)
	}
	leftovers(t, dir, "status.json")

	// --quiet silences the stderr line too; the exit code still says 3.
	install(t, okObs(2, 2))
	code, out, errs = run(t, "status", "--kubeconfig", "synthetic", "--quiet", "--out", bad)
	if code != 3 || out != "" || errs != "" {
		t.Errorf("quiet: code=%d out=%q err=%q", code, out, errs)
	}
}

func TestOutUsageErrors(t *testing.T) {
	cfgDir := t.TempDir()
	cfg := filepath.Join(cfgDir, "scry.toml")
	kc := filepath.Join(cfgDir, "k.kubeconfig")
	for _, p := range []string{cfg, kc} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cases := map[string][]string{
		"empty":              {"status", "--kubeconfig", "synthetic", "--out", ""},
		"stdout dash":        {"status", "--kubeconfig", "synthetic", "--out", "-"},
		"the kubeconfig":     {"status", "--kubeconfig", kc, "--out", kc},
		"the kubeconfig dot": {"status", "--kubeconfig", kc, "--out", filepath.Join(cfgDir, ".", "k.kubeconfig")},
		"the config":         {"status", "--config", cfg, "--out", cfg},
		"wait":               {"wait", "--for=readyz", "--kubeconfig", "synthetic", "--out", filepath.Join(t.TempDir(), "s.json")},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			s := install(t, okObs(1, 1))
			code, out, errs := run(t, args...)
			if code != 8 || out != "" || errs == "" {
				t.Fatalf("code=%d out=%q err=%q", code, out, errs)
			}
			if s.created != 0 {
				t.Fatal("a client was created for a usage error")
			}
		})
	}
	for _, p := range []string{cfg, kc} {
		if got := fileOf(t, p); got != "x" {
			t.Fatalf("a credential or config file was modified: %q", got)
		}
	}
}

// K3 (R9): once the rename has succeeded, a directory fsync failure is a stderr
// warning (silenced by --quiet) and the exit code follows the status result.
func TestDirSyncFailureIsAWarningAndExitFollowsStatus(t *testing.T) {
	old := writeOut
	t.Cleanup(func() { writeOut = old })
	for _, c := range []struct {
		name string
		obs  report.Observation
		code int
	}{{"green", okObs(2, 2), 0}, {"yellow", okObs(1, 2), 1}, {"red", readyzFailing(), 2}} {
		t.Run(c.name, func(t *testing.T) {
			install(t, c.obs)
			var wrote string
			writeOut = func(path string, data []byte) error {
				if err := outfile.Write(path, data); err != nil {
					return err
				}
				wrote = path
				return fmt.Errorf("sync directory: %w: input/output error", outfile.ErrDirSync)
			}
			dir, path := seed(t)
			code, out, errs := run(t, "status", "--kubeconfig", "synthetic", "--json", "--out", path)
			if code != c.code {
				t.Fatalf("code = %d, want %d (exit follows status)", code, c.code)
			}
			if err := schematest.Status([]byte(out)); err != nil || strings.Contains(out, "out_write_failed") {
				t.Fatalf("stdout is not the observed document: %v\n%s", err, out)
			}
			if !strings.HasPrefix(errs, "scry: warning: ") || strings.Count(errs, "\n") != 1 || strings.Contains(errs, dir) {
				t.Fatalf("stderr = %q, want one warning line without the path", errs)
			}
			if wrote == "" || fileOf(t, path) == sentinel {
				t.Fatal("the new file is not in place")
			}

			// --quiet: the warning is silenced too, the exit is unchanged.
			install(t, c.obs)
			code, out, errs = run(t, "status", "--kubeconfig", "synthetic", "--quiet", "--out", path)
			if code != c.code || out != "" || errs != "" {
				t.Fatalf("quiet: code=%d out=%q err=%q", code, out, errs)
			}
		})
	}
}
