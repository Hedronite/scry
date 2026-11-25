package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const credential = "apiVersion: v1\nkind: Config\n# synthetic credential file\n"

func plant(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(credential), 0o600); err != nil {
		t.Fatal(err)
	}
}

func link(t *testing.T, target, name string) {
	t.Helper()
	if err := os.Symlink(target, name); err != nil {
		t.Fatal(err)
	}
}

// refused runs status and expects the usage refusal: no client, no document,
// no path in the message, and the credential file as it was.
func refused(t *testing.T, protected string, args ...string) {
	t.Helper()
	s := install(t, okObs(1, 1))
	code, out, errs := run(t, append([]string{"status", "--json"}, args...)...)
	if code != 8 || out != "" || s.created != 0 || !strings.Contains(errs, "--out must not be") {
		t.Fatalf("%v: code=%d created=%d out=%q err=%q", args, code, s.created, out, errs)
	}
	if strings.Contains(errs, filepath.Dir(protected)) {
		t.Fatalf("the message carries a path: %q", errs)
	}
	if got := fileOf(t, protected); got != credential {
		t.Fatalf("%v: the credential file changed: %q", args, got)
	}
}

// --out must never replace a file $KUBECONFIG names, however it is spelled.
func TestOutRefusesEnvKubeconfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "real", "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	kc := filepath.Join(dir, "real", "kubeconfig")
	second := filepath.Join(dir, "real", "second")
	plant(t, kc)
	plant(t, second)
	link(t, kc, filepath.Join(dir, "file-link"))
	link(t, "kubeconfig", filepath.Join(dir, "real", "relative-link"))
	link(t, filepath.Join(dir, "real"), filepath.Join(dir, "dir-link"))
	link(t, filepath.Join(dir, "real", "sub"), filepath.Join(dir, "sub-link"))
	t.Chdir(dir)

	// "sub-link/.." is real/, not dir: a lexical clean would miss it.
	dotdot := "sub-link" + string(os.PathSeparator) + ".." + string(os.PathSeparator) + "kubeconfig"
	list := func(paths ...string) string { return strings.Join(paths, string(os.PathListSeparator)) }
	cases := map[string]struct{ env, out, protected string }{
		"direct":                {kc, kc, kc},
		"relative out":          {kc, filepath.Join("real", "kubeconfig"), kc},
		"relative env":          {filepath.Join("real", "kubeconfig"), kc, kc},
		"unclean":               {kc, dir + "/real/./sub/../kubeconfig", kc},
		"out is a link":         {kc, filepath.Join(dir, "file-link"), kc},
		"env is a link":         {filepath.Join(dir, "file-link"), kc, kc},
		"env is relative link":  {filepath.Join(dir, "real", "relative-link"), kc, kc},
		"linked directory":      {kc, filepath.Join(dir, "dir-link", "kubeconfig"), kc},
		"dot dot after a link":  {kc, dotdot, kc},
		"second in the list":    {list(kc, second), second, second},
		"after an empty entry":  {list("", kc), kc, kc},
		"first of a long list":  {list(kc, second), kc, kc},
		"linked list entry":     {list(second, filepath.Join(dir, "file-link")), kc, kc},
		"through linked parent": {filepath.Join(dir, "dir-link", "kubeconfig"), kc, kc},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("KUBECONFIG", c.env)
			refused(t, c.protected, "--allow-env-kubeconfig", "--out", c.out)
			// --kubeconfig taking over does not make the env files writable.
			refused(t, c.protected, "--allow-env-kubeconfig", "--kubeconfig", "synthetic", "--out", c.out)
		})
	}
}

// A $KUBECONFIG file that does not exist yet is protected too.
func TestOutRefusesAMissingEnvKubeconfig(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "not-yet")
	link(t, missing, filepath.Join(dir, "dangling"))
	link(t, dir, filepath.Join(dir, "dir-link"))
	for name, c := range map[string]struct{ env, out string }{
		"direct":           {missing, missing},
		"dangling link":    {filepath.Join(dir, "dangling"), missing},
		"linked directory": {missing, filepath.Join(dir, "dir-link", "not-yet")},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("KUBECONFIG", c.env)
			s := install(t, okObs(1, 1))
			code, out, _ := run(t, "status", "--allow-env-kubeconfig", "--out", c.out, "--json")
			if code != 8 || out != "" || s.created != 0 {
				t.Fatalf("code=%d created=%d out=%q", code, s.created, out)
			}
			if _, err := os.Stat(missing); err == nil {
				t.Fatal("the missing kubeconfig was created")
			}
		})
	}
}

// A relative --out is resolved from the real working directory, not from the
// name the shell reached it by.
func TestOutRefusesAMissingEnvKubeconfigFromALinkedDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "real"), 0o700); err != nil {
		t.Fatal(err)
	}
	link(t, filepath.Join(dir, "real"), filepath.Join(dir, "way-in"))
	missing := filepath.Join(dir, "real", "not-yet")
	t.Setenv("KUBECONFIG", missing)
	t.Chdir(filepath.Join(dir, "way-in"))
	s := install(t, okObs(1, 1))
	code, out, _ := run(t, "status", "--allow-env-kubeconfig", "--out", "not-yet", "--json")
	if code != 8 || out != "" || s.created != 0 {
		t.Fatalf("code=%d created=%d out=%q", code, s.created, out)
	}
	if _, err := os.Stat(missing); err == nil {
		t.Fatal("the missing kubeconfig was created")
	}
}

// A kubeconfig that came from a config entry is resolved the same way.
func TestOutRefusesTheConfigEntryKubeconfigThroughALink(t *testing.T) {
	dir := t.TempDir()
	kc := filepath.Join(dir, "kubeconfig")
	plant(t, kc)
	link(t, kc, filepath.Join(dir, "file-link"))
	cfg := writeConfig(t, "schema = \"scry.config/v0\"\n[[cluster]]\nalias = \"solo\"\nkubeconfig = '"+filepath.Join(dir, "file-link")+"'\n")
	refused(t, kc, "--config", cfg, "--out", kc)
}

// A distinct --out beside the credential is written as before.
func TestOutBesideEnvKubeconfigSucceeds(t *testing.T) {
	dir := t.TempDir()
	kc := filepath.Join(dir, "kubeconfig")
	plant(t, kc)
	t.Setenv("KUBECONFIG", kc)
	for _, name := range []string{"status.json", "kubeconfig.json"} {
		install(t, okObs(1, 1))
		out := filepath.Join(dir, name)
		code, _, errs := run(t, "status", "--allow-env-kubeconfig", "--out", out, "--json")
		if code != 0 || !strings.Contains(fileOf(t, out), `"scry.status/v0"`) {
			t.Fatalf("code=%d err=%q", code, errs)
		}
	}
	if got := fileOf(t, kc); got != credential {
		t.Fatalf("the credential file changed: %q", got)
	}
}
