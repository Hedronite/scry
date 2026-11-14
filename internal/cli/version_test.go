package cli

import (
	"encoding/json"
	"testing"

	"github.com/Hedronite/scry/internal/schematest"
)

// An unstamped build says so; it never invents a version.
func TestUnstampedBuildReportsDevel(t *testing.T) {
	if version != "devel" {
		t.Fatalf("default version = %q, want devel", version)
	}
	_, out, _ := run(t, "version")
	if got := "scry devel"; len(out) < len(got) || out[:len(got)] != got {
		t.Fatalf("out = %q", out)
	}
}

// The release stamps the tag as given (v-prefixed); both forms carry it and the
// JSON still validates against scry.version/v0.
func TestStampedVersionIsReportedAsGiven(t *testing.T) {
	old := version
	version = "v1.2.3"
	t.Cleanup(func() { version = old })

	_, out, _ := run(t, "version")
	if want := "scry v1.2.3 ("; len(out) < len(want) || out[:len(want)] != want {
		t.Fatalf("out = %q", out)
	}
	_, js, _ := run(t, "version", "--json")
	if err := schematest.Version([]byte(js)); err != nil {
		t.Fatalf("%v\n%s", err, js)
	}
	var v VersionDoc
	if err := json.Unmarshal([]byte(js), &v); err != nil || v.Version != "v1.2.3" {
		t.Fatalf("%v %+v", err, v)
	}
}
