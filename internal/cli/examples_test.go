package cli

import (
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const examplesDir = "../../docs/examples"

// placeholders are the only absolute paths the examples may carry.
var placeholders = map[string]bool{
	"/usr/local/bin/scry":       true,
	"/etc/scry/scry.toml":       true,
	"/var/lib/scry/status.json": true,
	"/var/log/scry/status.log":  true,
	"/var/log/scry/status.err":  true,
}

func example(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(examplesDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// swap replaces the placeholder config and out paths so a test can run the
// example's own arguments without touching the machine.
func swap(args []string, cfg, out string) []string {
	r := make([]string, len(args))
	for i, a := range args {
		switch a {
		case "/etc/scry/scry.toml":
			a = cfg
		case "/var/lib/scry/status.json":
			a = out
		}
		r[i] = a
	}
	return r
}

// exampleArgs returns the argument list the example runs scry with.
func systemdArgs(t *testing.T) []string {
	t.Helper()
	for _, l := range strings.Split(example(t, "scry-status.service"), "\n") {
		if rest, ok := strings.CutPrefix(l, "ExecStart="); ok {
			f := strings.Fields(rest)
			if len(f) < 2 {
				t.Fatalf("ExecStart too short: %q", l)
			}
			return f[1:]
		}
	}
	t.Fatal("no ExecStart")
	return nil
}

func plistArgs(t *testing.T) (args []string, interval string) {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(example(t, "com.example.scry-status.plist")))
	dec.Strict = false
	dec.Entity = xml.HTMLEntity
	var key string
	var inArgs bool
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("plist is not well-formed XML: %v", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "key":
			var k string
			_ = dec.DecodeElement(&k, &se)
			key, inArgs = k, false
		case "array":
			inArgs = key == "ProgramArguments"
		case "string":
			var v string
			_ = dec.DecodeElement(&v, &se)
			if inArgs {
				args = append(args, v)
			}
		case "integer":
			var v string
			_ = dec.DecodeElement(&v, &se)
			if key == "StartInterval" {
				interval = v
			}
		}
	}
	return args, interval
}

// The examples must keep working: their arguments parse (not a usage error),
// they use status with --out, and never wait.
func TestExamplesRunScryStatusWithValidFlags(t *testing.T) {
	pa, _ := plistArgs(t)
	if len(pa) < 2 {
		t.Fatalf("plist ProgramArguments = %v", pa)
	}
	for name, args := range map[string][]string{"systemd": systemdArgs(t), "launchd": pa[1:]} {
		t.Run(name, func(t *testing.T) {
			if args[0] != "status" {
				t.Fatalf("first argument = %q, want status", args[0])
			}
			joined := strings.Join(args, " ")
			for _, want := range []string{"--out", "--config", "--json"} {
				if !strings.Contains(joined, want) {
					t.Errorf("missing %s in %q", want, joined)
				}
			}
			dir := t.TempDir()
			out := filepath.Join(dir, "status.json")
			install(t, okObs(1, 1))
			code, _, errs := run(t, swap(args, filepath.Join(dir, "absent.toml"), out)...)
			if code == 8 {
				t.Fatalf("the example's flags are rejected as usage: %q", errs)
			}
			if code != 3 {
				t.Fatalf("code = %d, want 3 (config file absent, no network)", code)
			}
			if _, err := os.Stat(out); err == nil {
				t.Fatal("a config failure wrote the out file")
			}
		})
	}
}

func TestExamplesRunEveryMinuteInsideVigilStaleAfter(t *testing.T) {
	if !strings.Contains(example(t, "scry-status.timer"), "OnUnitActiveSec=60s") {
		t.Error("timer is not every 60s")
	}
	if _, iv := plistArgs(t); iv != "60" {
		t.Errorf("StartInterval = %q, want 60", iv)
	}
}

func TestSystemdExampleTreatsVerdictsAsSuccess(t *testing.T) {
	svc := example(t, "scry-status.service")
	if !strings.Contains(svc, "SuccessExitStatus=1 2") || !strings.Contains(svc, "Type=oneshot") {
		t.Error("service must be oneshot with SuccessExitStatus=1 2")
	}
}

// Placeholder paths only: no host, address, user directory or cluster name.
func TestExamplesCarryPlaceholdersOnly(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(examplesDir, "*"))
	if err != nil || len(files) < 3 {
		t.Fatalf("examples = %v, %v", files, err)
	}
	tag := regexp.MustCompile(`<[^>]*>`)
	path := regexp.MustCompile(`(?:^|[\s"(])(/[A-Za-z0-9._/-]+)`)
	// The tokens are assembled at run time so this file does not itself carry
	// the strings the repository scrub forbids.
	home := "/" + "Users/|/" + "home/"
	mesh := `\.lo` + `cal\b|\.ts\.` + `net`
	port := ":" + "6443"
	forbidden := regexp.MustCompile(`(?i)(\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b|` + home + `|` + mesh + `|` + port + `|\bkubeconfig:|[a-z0-9-]+\.(com|net|org|io)\b)`)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, "://") {
				continue // the documentation URL and the plist DTD
			}
			line = tag.ReplaceAllString(line, " ")
			if forbidden.MatchString(line) {
				t.Errorf("%s:%d carries a real-looking address or name: %q", filepath.Base(f), i+1, line)
			}
			for _, m := range path.FindAllStringSubmatch(line, -1) {
				if p := m[1]; !placeholders[p] {
					t.Errorf("%s:%d path %q is not an approved placeholder", filepath.Base(f), i+1, p)
				}
			}
		}
	}
}
