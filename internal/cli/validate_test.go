package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateAcceptsAGoodFileWithoutTouchingACluster(t *testing.T) {
	for name, path := range map[string]string{
		"declared":    declared(t),
		"undeclared":  writeConfig(t, twoClusters),
		"no clusters": writeConfig(t, "schema = \"scry.config/v0\"\n"),
	} {
		t.Run(name, func(t *testing.T) {
			// The kubeconfig paths do not exist: validate must not open them.
			s := install(t, okObs(1, 1))
			code, out, errs := run(t, "validate", "--config", path)
			if code != 0 || errs != "" || !strings.HasPrefix(out, "ok: scry.config/v0, clusters: ") {
				t.Fatalf("code=%d out=%q err=%q", code, out, errs)
			}
			if s.created != 0 || s.obs.calls != 0 {
				t.Fatal("validate built a client")
			}
			if strings.Contains(out, filepath.Dir(path)) {
				t.Fatalf("output carries the path: %q", out)
			}
		})
	}
}

func TestValidateRefusesABadFile(t *testing.T) {
	const head = "schema = \"scry.config/v0\"\n"
	for name, c := range map[string]struct{ body, want string }{
		"bad declaration": {head + "declaration = \"sha256:abc\"\n", "declaration"},
		"unknown key":     {head + "server = \"x\"\n", "unknown key"},
		"miscased key":    {head + "Declaration = \"" + digest + "\"\n", "unknown key"},
		"toml null":       {head + "declaration = null\n", "not valid TOML"},
		"wrong schema":    {"schema = \"scry.config/v1\"\n", "schema"},
	} {
		t.Run(name, func(t *testing.T) {
			s := install(t, okObs(1, 1))
			path := writeConfig(t, c.body)
			code, out, errs := run(t, "validate", "--config", path)
			if code != 3 || out != "" || !strings.Contains(errs, c.want) || s.created != 0 {
				t.Fatalf("code=%d out=%q err=%q", code, out, errs)
			}
			if strings.Contains(errs, filepath.Dir(path)) || strings.Contains(errs, "sha256:abc") {
				t.Fatalf("message carries the path or the content: %q", errs)
			}
		})
	}
	if code, out, _ := run(t, "validate", "--config", filepath.Join(t.TempDir(), "absent.toml")); code != 3 || out != "" {
		t.Fatalf("absent file: code=%d out=%q", code, out)
	}
}

func TestValidateUsage(t *testing.T) {
	for _, args := range [][]string{
		{"validate"},
		{"validate", "--config", ""},
		{"validate", "--kubeconfig", "x"},
		{"validate", "extra"},
	} {
		if code, out, errs := run(t, args...); code != 8 || out != "" || errs == "" {
			t.Errorf("%v: code=%d out=%q err=%q", args, code, out, errs)
		}
	}
}
