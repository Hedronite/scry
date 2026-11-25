package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Hedronite/scry/internal/exitcode"
	"github.com/Hedronite/scry/internal/report"
)

const good = `
schema = "scry.config/v0"

[[cluster]]
alias = "lab-one"
kubeconfig = "/synthetic/lab-one.kubeconfig"
expected = "dark"
timeout = "2s"

[[cluster]]
alias = "lab-two"
kubeconfig = "/synthetic/lab-two.kubeconfig"
`

func TestParseGood(t *testing.T) {
	c, err := Parse([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Clusters) != 2 {
		t.Fatalf("clusters = %d", len(c.Clusters))
	}
	one, err := c.Lookup("lab-one")
	if err != nil || one.Expected != report.Dark || one.Timeout != 2*time.Second {
		t.Fatalf("lab-one = %+v, %v", one, err)
	}
	two, _ := c.Lookup("lab-two")
	if two.Expected != report.Light || two.Timeout != 5*time.Second {
		t.Fatalf("defaults wrong: %+v", two)
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"no schema":        "[[cluster]]\nalias='a'\nkubeconfig='/k'\n",
		"wrong schema":     "schema='scry.config/v1'\n",
		"unknown key":      "schema='scry.config/v0'\n[[cluster]]\nalias='a'\nkubeconfig='/k'\ntoken='secret'\n",
		"unknown top key":  "schema='scry.config/v0'\nserver='x'\n",
		"bad alias":        "schema='scry.config/v0'\n[[cluster]]\nalias='A_b'\nkubeconfig='/k'\n",
		"url alias":        "schema='scry.config/v0'\n[[cluster]]\nalias='https://h.example'\nkubeconfig='/k'\n",
		"missing path":     "schema='scry.config/v0'\n[[cluster]]\nalias='a'\n",
		"bad expected":     "schema='scry.config/v0'\n[[cluster]]\nalias='a'\nkubeconfig='/k'\nexpected='off'\n",
		"timeout too low":  "schema='scry.config/v0'\n[[cluster]]\nalias='a'\nkubeconfig='/k'\ntimeout='1ms'\n",
		"timeout too high": "schema='scry.config/v0'\n[[cluster]]\nalias='a'\nkubeconfig='/k'\ntimeout='5m'\n",
		"timeout garbage":  "schema='scry.config/v0'\n[[cluster]]\nalias='a'\nkubeconfig='/k'\ntimeout='soon'\n",
		"duplicate alias":  "schema='scry.config/v0'\n[[cluster]]\nalias='a'\nkubeconfig='/k'\n[[cluster]]\nalias='a'\nkubeconfig='/j'\n",
		"not toml":         "this is = = not toml",
		"oversize":         "schema='scry.config/v0'\n#" + strings.Repeat("x", maxFileBytes),
	}
	for name, in := range cases {
		_, err := Parse([]byte(in))
		if err == nil {
			t.Errorf("%s: accepted, want rejection", name)
			continue
		}
		if !errors.Is(err, &exitcode.Error{Reason: exitcode.ConfigInvalid}) || exitcode.CodeOf(err) != exitcode.Config {
			t.Errorf("%s: err = %v, want config_invalid / exit 3", name, err)
		}
	}
}

// TestErrorsDoNotEchoContent: a secret-looking value in a rejected file must
// not appear in the error text.
func TestErrorsDoNotEchoContent(t *testing.T) {
	_, err := Parse([]byte("schema='scry.config/v0'\n[[cluster]]\nalias='a'\nkubeconfig='/k'\ntoken='SECRETVALUE123'\n"))
	if err == nil || strings.Contains(err.Error(), "SECRETVALUE123") {
		t.Fatalf("err = %v", err)
	}
	_, err = Parse([]byte("x = 'SECRETVALUE123' = ="))
	if err == nil || strings.Contains(err.Error(), "SECRETVALUE123") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadMissingFileHidesPath(t *testing.T) {
	p := filepath.Join(t.TempDir(), "private-name.toml")
	_, err := Load(p)
	if err == nil || strings.Contains(err.Error(), "private-name") {
		t.Fatalf("err = %v", err)
	}
	if exitcode.CodeOf(err) != exitcode.Config {
		t.Fatalf("code = %d", exitcode.CodeOf(err))
	}
}

func TestLoadFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.toml")
	if err := os.WriteFile(p, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err != nil {
		t.Fatal(err)
	}
}

func TestLookupMissingListsAliases(t *testing.T) {
	c, _ := Parse([]byte(good))
	_, err := c.Lookup("nope")
	e, ok := exitcode.Of(err)
	if !ok || len(e.Available) != 2 {
		t.Fatalf("err = %v", err)
	}
}

func TestDeclaration(t *testing.T) {
	const head = "schema='scry.config/v0'\n"
	hex := strings.Repeat("0123456789abcdef", 4)
	if c, err := Parse([]byte(head)); err != nil || c.Declaration != nil {
		t.Fatalf("absent: %+v %v", c, err)
	}
	if c, err := Parse([]byte(head + "declaration='sha256:" + hex + "'\n")); err != nil || c.Declaration == nil || *c.Declaration != "sha256:"+hex {
		t.Fatalf("digest: %+v %v", c, err)
	}
	for _, value := range []string{
		"''", "'null'", "null", "7", "true", "['sha256:" + hex + "']", "'" + hex + "'", "'sha1:" + hex + "'",
		"'sha256:" + hex[1:] + "'", "'sha256:" + hex + "0'", "'sha256:" + strings.ToUpper(hex) + "'",
		"' sha256:" + hex + "'", "\"sha256:" + hex + "\\n\"",
	} {
		_, err := Parse([]byte(head + "declaration=" + value + "\n"))
		if err == nil || exitcode.CodeOf(err) != exitcode.Config {
			t.Errorf("declaration=%s: err = %v, want exit 3", value, err)
		}
	}
}

// Keys are exact: the decoder alone would match them without regard to case.
func TestKeysAreCaseSensitive(t *testing.T) {
	const head = "schema='scry.config/v0'\n"
	const entry = "alias='a'\nkubeconfig='/k'\n"
	for name, in := range map[string]string{
		"top schema":      "SCHEMA='scry.config/v0'\n",
		"top declaration": head + "Declaration='sha256:" + strings.Repeat("a", 64) + "'\n",
		"table name":      head + "[[Cluster]]\n" + entry,
		"entry key":       head + "[[cluster]]\nALIAS='a'\nkubeconfig='/k'\n",
		"override":        head + "[[cluster]]\n" + entry + "Alias='b'\n",
		"inline table":    head + "cluster=[{Alias='a', kubeconfig='/k'}]\n",
		"dotted":          head + "cluster.Alias='a'\n",
		"single table":    head + "[cluster]\n" + entry,
		"nested":          head + "[[cluster]]\n" + entry + "[cluster.extra]\nx=1\n",
	} {
		if _, err := Parse([]byte(in)); err == nil || exitcode.CodeOf(err) != exitcode.Config {
			t.Errorf("%s: err = %v, want exit 3", name, err)
		}
	}
	if c, err := Parse([]byte(head + "cluster=[{alias='a', kubeconfig='/k'}]\n")); err != nil || len(c.Clusters) != 1 {
		t.Fatalf("an inline array of exact keys is valid TOML for the same value: %+v %v", c, err)
	}
}
