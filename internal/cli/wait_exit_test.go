package cli

import (
	"strings"
	"testing"

	"github.com/Hedronite/scry/internal/exitcode"
	"github.com/Hedronite/scry/internal/report"
)

// wait's exit code follows its help: ready 0, ready but degraded 1 (whatever
// the severity), timeout 2, and the fail-fast codes. status keeps severity.
func TestWaitExitMatchesHelp(t *testing.T) {
	failing := func(r exitcode.Reason) report.Observation {
		return report.Observation{Apiserver: report.Apiserver{Result: report.Up}, Failure: exitcode.New(r, "synthetic")}
	}
	cases := []struct {
		name     string
		obs      report.Observation
		wait     int
		status   int
		severity string
	}{
		{"ready", okObs(2, 2), 0, 0, `"severity": "GREEN"`},
		{"one of two ready", okObs(1, 2), 1, 1, `"severity": "YELLOW"`},
		{"no node ready", okObs(0, 3), 1, 2, `"severity": "RED"`},
		{"no nodes", okObs(0, 0), 1, 2, `"severity": "RED"`},
		{"readyz failing", readyzFailing(), 2, 2, `"severity": "RED"`},
		{"unreachable", darkObs(), 2, 2, `"severity": "RED"`},
		{"silent peer", failing(exitcode.HTTPUnresponsive), 2, 6, `"observed": false`},
		{"auth", failing(exitcode.AuthRejected), 4, 4, `"observed": false`},
		{"tls", failing(exitcode.TLSVerifyFailed), 5, 5, `"observed": false`},
		{"config", failing(exitcode.KubeconfigInvalid), 3, 3, `"observed": false`},
		{"internal", failing(exitcode.InternalError), 7, 7, `"observed": false`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			install(t, c.obs)
			code, out, errs := run(t, "wait", "--for", "readyz", "--kubeconfig", "synthetic", "--timeout", "2s", "--json")
			if code != c.wait || !strings.Contains(out, c.severity) {
				t.Errorf("wait: code=%d want %d, err=%q out=%s", code, c.wait, errs, out)
			}
			install(t, c.obs)
			if code, _, _ := run(t, "status", "--kubeconfig", "synthetic", "--quiet"); code != c.status {
				t.Errorf("status: code=%d want %d", code, c.status)
			}
		})
	}
	if !strings.Contains(waitLong, "ready but degraded exits 1, even when") {
		t.Error("help no longer states the degraded exit")
	}
}
