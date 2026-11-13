package report_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Hedronite/scry/internal/exitcode"
	"github.com/Hedronite/scry/internal/report"
)

const fixtureDir = "../../testdata"

// requiredValid and requiredInvalid are the K0 golden set. Each name is a
// state a consumer must be able to rely on.
var (
	requiredValid = []string{
		"green", "yellow", "red", "expected-dark",
		"unobserved-auth", "unobserved-tls", "wait-timeout",
		"yellow-unknown", "red-all-unknown", "wait-ready",
	}
	requiredInvalidKinds = 4
)

func TestFixtureSetComplete(t *testing.T) {
	for _, n := range requiredValid {
		if _, err := os.Stat(filepath.Join(fixtureDir, "status", n+".json")); err != nil {
			t.Errorf("missing golden fixture %s: %v", n, err)
		}
	}
	files, err := filepath.Glob(filepath.Join(fixtureDir, "status-invalid", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < requiredInvalidKinds {
		t.Errorf("status-invalid has %d fixtures, want at least %d", len(files), requiredInvalidKinds)
	}
}

// TestBuildReproducesFixtures ties the golden documents to the readiness
// model: feed Build only the raw evidence from each fixture and it must give
// back the fixture's verdict. A fixture that disagrees with the severity rules
// (spec section 5) fails here, so the goldens cannot teach consumers a wrong
// contract.
func TestBuildReproducesFixtures(t *testing.T) {
	for _, name := range requiredValid {
		t.Run(name, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join(fixtureDir, "status", name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var want report.Document
			if err := json.Unmarshal(b, &want); err != nil {
				t.Fatal(err)
			}
			obs := report.Observation{
				Cluster: want.Cluster, Server: want.Server, Apiserver: want.Apiserver,
				Readyz: want.Readyz, Nodes: want.Nodes,
			}
			if want.Error != nil {
				obs.Failure = exitcode.New(want.Error.Code, "%s", want.Error.Message)
			}
			got := report.Build(obs, time.Date(2026, 11, 13, 20, 0, 0, 0, time.UTC))

			wantReasons := want.Reasons
			if want.Wait != nil && want.Wait.Outcome == report.WaitTimeout {
				// The wait loop, not Build, adds the timeout verdict (K1):
				// Build must reproduce the last poll underneath it.
				wantReasons = slices.DeleteFunc(slices.Clone(wantReasons),
					func(r exitcode.Reason) bool { return r == exitcode.Timeout })
			}
			if got.Observed != want.Observed {
				t.Errorf("observed = %v, fixture says %v", got.Observed, want.Observed)
			}
			if got.Severity != want.Severity {
				t.Errorf("severity = %q, fixture says %q", got.Severity, want.Severity)
			}
			if !slices.Equal(got.Reasons, wantReasons) {
				t.Errorf("reasons = %v, fixture says %v", got.Reasons, wantReasons)
			}
			if (got.Error == nil) != (want.Error == nil) ||
				(got.Error != nil && got.Error.Code != want.Error.Code) {
				t.Errorf("error = %+v, fixture says %+v", got.Error, want.Error)
			}
			if strings.Contains(name, "wait") && want.Wait == nil {
				t.Error("wait fixture has no wait block")
			}
		})
	}
}
