package config

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/Hedronite/scry/internal/schematest"
)

// asJSON is the JSON value a TOML file describes, which the schema judges.
func asJSON(t *testing.T, body string) []byte {
	t.Helper()
	var doc map[string]any
	if err := toml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("%q: %v", body, err)
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The published schema and the loader agree on what a config file may be.
func TestSchemaAgreesWithLoader(t *testing.T) {
	const head = "schema='scry.config/v0'\n"
	entry := func(extra string) string { return head + "[[cluster]]\nalias='a'\nkubeconfig='/k'\n" + extra }
	cases := map[string]struct {
		body string
		ok   bool
	}{
		"minimal":             {head, true},
		"good":                {good, true},
		"declared":            {head + "declaration='sha256:" + strings.Repeat("f", 64) + "'\n", true},
		"empty expected":      {entry("expected=''\n"), true},
		"empty timeout":       {entry("timeout=''\n"), true},
		"compound timeout":    {entry("timeout='1.5s500ms'\n"), true},
		"microseconds":        {entry("timeout='200000us'\n"), true},
		"no schema":           {"declaration='sha256:" + strings.Repeat("f", 64) + "'\n", false},
		"wrong schema":        {"schema='scry.config/v1'\n", false},
		"unknown top key":     {head + "server='x'\n", false},
		"unknown entry key":   {entry("token='x'\n"), false},
		"miscased key":        {entry("Alias='b'\n"), false},
		"bad declaration":     {head + "declaration='sha256:abc'\n", false},
		"numeric declaration": {head + "declaration=7\n", false},
		"bad alias":           {head + "[[cluster]]\nalias='A_b'\nkubeconfig='/k'\n", false},
		"no alias":            {head + "[[cluster]]\nkubeconfig='/k'\n", false},
		"no kubeconfig":       {head + "[[cluster]]\nalias='a'\n", false},
		"empty kubeconfig":    {head + "[[cluster]]\nalias='a'\nkubeconfig=''\n", false},
		"bad expected":        {entry("expected='off'\n"), false},
		"timeout garbage":     {entry("timeout='soon'\n"), false},
		"timeout no unit":     {entry("timeout='5'\n"), false},
		"negative timeout":    {entry("timeout='-5s'\n"), false},
		"timeout number":      {entry("timeout=5\n"), false},
		"cluster not a list":  {head + "cluster='a'\n", false},
	}
	for name, c := range cases {
		_, loadErr := Parse([]byte(c.body))
		schemaErr := schematest.Config(asJSON(t, c.body))
		if (loadErr == nil) != c.ok || (schemaErr == nil) != c.ok {
			t.Errorf("%s: want ok=%v, loader %v, schema %v", name, c.ok, loadErr, schemaErr)
		}
	}
	// null is part of the JSON schema although TOML cannot spell it.
	if err := schematest.Config([]byte(`{"schema":"scry.config/v0","declaration":null}`)); err != nil {
		t.Errorf("null declaration: %v", err)
	}
}
