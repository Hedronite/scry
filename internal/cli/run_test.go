package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Hedronite/scry/internal/exitcode"
	"github.com/Hedronite/scry/internal/kube"
	"github.com/Hedronite/scry/internal/report"
	"github.com/Hedronite/scry/internal/schematest"
)

func ptr[T any](v T) *T { return &v }

// fakeObserver replays observations, repeating the last, and records calls.
type fakeObserver struct {
	cluster report.Cluster
	obs     []report.Observation
	calls   int
}

func (f *fakeObserver) Cluster() report.Cluster { return f.cluster }
func (f *fakeObserver) Observe(context.Context) report.Observation {
	i := min(f.calls, len(f.obs)-1)
	f.calls++
	o := f.obs[i]
	o.Cluster = f.cluster
	return o
}

type fakeClock struct {
	now    time.Time
	sleeps int
}

func (c *fakeClock) Now() time.Time { return c.now }
func (c *fakeClock) Sleep(_ context.Context, d time.Duration) error {
	c.sleeps++
	c.now = c.now.Add(d)
	return nil
}

func okObs(ready, total int) report.Observation {
	items := make([]report.Node, total)
	for i := range items {
		items[i] = report.Node{Name: "node-" + string(rune('a'+i)), Ready: i < ready}
	}
	return report.Observation{
		Apiserver: report.Apiserver{Result: report.Up, HTTPStatus: ptr(200)},
		Server:    &report.Server{Version: "v1.34.0", Flavor: "kubernetes"},
		Readyz:    &report.Readyz{Mode: report.ModeAggregate, OK: true, Checks: []report.Check{}},
		Nodes:     &report.Nodes{Total: total, Ready: ready, Items: items},
	}
}

func readyzFailing() report.Observation {
	o := okObs(1, 1)
	o.Readyz.OK = false
	return o
}

func darkObs() report.Observation {
	return report.Observation{Apiserver: report.Apiserver{Result: report.Down}}
}

type seam struct {
	opts    []kube.Options
	obs     *fakeObserver
	newErr  error
	clk     *fakeClock
	created int
}

// install swaps the package seams for one test.
func install(t *testing.T, obs ...report.Observation) *seam {
	t.Helper()
	s := &seam{
		obs: &fakeObserver{cluster: report.Cluster{Alias: "lab", Expected: report.Light}, obs: obs},
		clk: &fakeClock{now: time.Date(2026, 11, 13, 20, 0, 0, 0, time.UTC)},
	}
	oldNew, oldNow, oldClk := newObserver, nowFunc, clock
	newObserver = func(o kube.Options) (Observer, error) {
		s.created++
		s.opts = append(s.opts, o)
		if s.newErr != nil {
			return nil, s.newErr
		}
		s.obs.cluster = report.Cluster{Alias: report.ResolveAlias(o.Cluster, "lab"), Expected: expectedOf(o)}
		return s.obs, nil
	}
	nowFunc = func() time.Time { return s.clk.now }
	clock = s.clk
	t.Cleanup(func() { newObserver, nowFunc, clock = oldNew, oldNow, oldClk })
	return s
}

func expectedOf(o kube.Options) report.Expected {
	if o.Expected == report.Dark {
		return report.Dark
	}
	return report.Light
}

func TestStatusExitFollowsSeverityAndPrintsTable(t *testing.T) {
	cases := []struct {
		name string
		obs  report.Observation
		code int
		want string
	}{
		{"green", okObs(2, 2), 0, "GREEN"},
		{"yellow", okObs(1, 2), 1, "YELLOW"},
		{"red", readyzFailing(), 2, "RED"},
	}
	for _, c := range cases {
		install(t, c.obs)
		code, out, errs := run(t, "status", "--kubeconfig", "synthetic")
		if code != c.code || !strings.Contains(out, c.want) || errs != "" {
			t.Errorf("%s: code=%d out=%q err=%q", c.name, code, out, errs)
		}
	}
}

func TestStatusJSONIsOneValidDocument(t *testing.T) {
	install(t, okObs(1, 2))
	code, out, errs := run(t, "status", "--kubeconfig", "synthetic", "--json")
	if code != 1 || errs != "" {
		t.Fatalf("code=%d err=%q", code, errs)
	}
	if err := schematest.Status([]byte(out)); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	dec := json.NewDecoder(strings.NewReader(out))
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatal(err)
	}
	if err := dec.Decode(&v); err != io.EOF {
		t.Fatalf("stdout holds more than one document: %v", err)
	}
}

func TestQuietPrintsNothingAnywhere(t *testing.T) {
	install(t, okObs(1, 2))
	code, out, errs := run(t, "status", "--kubeconfig", "synthetic", "--quiet")
	if code != 1 || out != "" || errs != "" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errs)
	}
	install(t, darkObs())
	s := install(t, okObs(1, 1))
	s.newErr = exitcode.New(exitcode.KubeconfigInvalid, "bad")
	code, out, errs = run(t, "status", "--kubeconfig", "synthetic", "--quiet")
	if code != 3 || out != "" || errs != "" {
		t.Fatalf("failure under --quiet: code=%d out=%q err=%q", code, out, errs)
	}
}

func TestExpectedDarkIsGreenAndReachesTheClient(t *testing.T) {
	s := install(t, darkObs())
	code, out, _ := run(t, "status", "--kubeconfig", "synthetic", "--expected", "dark", "--json")
	if code != 0 || len(s.opts) != 1 || s.opts[0].Expected != report.Dark {
		t.Fatalf("code=%d opts=%+v out=%s", code, s.opts, out)
	}
	if !strings.Contains(out, `"expected_dark"`) || !strings.Contains(out, `"GREEN"`) {
		t.Fatalf("out = %s", out)
	}
	install(t, darkObs())
	code, _, _ = run(t, "status", "--kubeconfig", "synthetic")
	if code != 2 {
		t.Fatalf("light + connect failure: code = %d, want 2", code)
	}
}

func TestFailureBeforeAnyRequestIsAnUnobservedDocument(t *testing.T) {
	s := install(t, okObs(1, 1))
	s.newErr = exitcode.New(exitcode.AuthRejected, "the server rejected the credentials")
	code, out, _ := run(t, "status", "--kubeconfig", "synthetic", "--json")
	if code != 4 {
		t.Fatalf("code = %d", code)
	}
	if err := schematest.Status([]byte(out)); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{`"observed": false`, `"result": "unknown"`, `"auth_rejected"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %s", want, out)
		}
	}
	if strings.Contains(out, `"severity"`) {
		t.Errorf("severity present on an unobserved document: %s", out)
	}
}

func TestContextMissListsAvailableOnStderrOnly(t *testing.T) {
	s := install(t, okObs(1, 1))
	e := exitcode.New(exitcode.KubeconfigInvalid, "that context is not in the kubeconfig")
	e.Available = []string{"ctx-a", "ctx-b"}
	s.newErr = e
	code, out, errs := run(t, "status", "--kubeconfig", "synthetic", "--context", "nope", "--json")
	if code != 3 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(errs, "ctx-a, ctx-b") || strings.Contains(out, "ctx-a") {
		t.Fatalf("out=%q err=%q", out, errs)
	}
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "scry.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const twoClusters = `schema = "scry.config/v0"

[[cluster]]
alias = "lab-one"
kubeconfig = "/synthetic/one"
expected = "dark"
timeout = "2s"

[[cluster]]
alias = "lab-two"
kubeconfig = "/synthetic/two"
`

func TestConfigResolution(t *testing.T) {
	cfg := writeConfig(t, twoClusters)

	s := install(t, okObs(1, 1))
	code, out, errs := run(t, "status", "--config", cfg, "--json")
	if code != 3 || s.created != 0 || !strings.Contains(errs, "lab-one, lab-two") {
		t.Fatalf("several clusters, none chosen: code=%d created=%d err=%q", code, s.created, errs)
	}
	if err := schematest.Status([]byte(out)); err != nil {
		t.Fatal(err)
	}

	s = install(t, okObs(1, 1))
	run(t, "status", "--config", cfg, "--cluster", "lab-one")
	o := s.opts[0]
	if o.Kubeconfig != "/synthetic/one" || o.Expected != report.Dark || o.Timeout != 2*time.Second || o.Cluster != "lab-one" {
		t.Fatalf("entry not applied: %+v", o)
	}

	s = install(t, okObs(1, 1))
	run(t, "status", "--config", cfg, "--cluster", "lab-one", "--expected", "light", "--kubeconfig", "/override")
	o = s.opts[0]
	if o.Kubeconfig != "/override" || o.Expected != report.Light {
		t.Fatalf("flags must beat the entry: %+v", o)
	}

	s = install(t, okObs(1, 1))
	code, _, errs = run(t, "status", "--config", cfg, "--cluster", "nope")
	if code != 3 || s.created != 0 || !strings.Contains(errs, "lab-one, lab-two") {
		t.Fatalf("unknown cluster: code=%d err=%q", code, errs)
	}

	one := writeConfig(t, "schema = \"scry.config/v0\"\n[[cluster]]\nalias = \"solo\"\nkubeconfig = \"/synthetic/solo\"\n")
	s = install(t, okObs(1, 1))
	run(t, "status", "--config", one)
	if s.created != 1 || s.opts[0].Cluster != "solo" {
		t.Fatalf("a single entry is selected without --cluster: %+v", s.opts)
	}

	s = install(t, okObs(1, 1))
	code, _, _ = run(t, "status", "--config", filepath.Join(t.TempDir(), "absent.toml"))
	if code != 3 || s.created != 0 {
		t.Fatalf("missing config file: code=%d", code)
	}
}

func TestRealClientRefusesMissingKubeconfigBeforeAnyNetwork(t *testing.T) {
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "from-env"))
	for _, args := range [][]string{
		{"status"},
		{"status", "--json"},
		{"wait", "--for", "readyz"},
	} {
		code, out, errs := run(t, args...)
		if code != 3 {
			t.Errorf("%v: code = %d, want 3 (out %q err %q)", args, code, out, errs)
		}
		if strings.Contains(out+errs, "from-env") {
			t.Errorf("%v: the KUBECONFIG path leaked: %q %q", args, out, errs)
		}
	}
}

func TestWaitReadyReportsTheWaitBlock(t *testing.T) {
	s := install(t, readyzFailing(), okObs(2, 2))
	code, out, _ := run(t, "wait", "--for", "readyz", "--kubeconfig", "synthetic", "--json")
	if code != 0 || s.clk.sleeps != 1 {
		t.Fatalf("code=%d sleeps=%d out=%s", code, s.clk.sleeps, out)
	}
	if err := schematest.Status([]byte(out)); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, `"outcome": "ready"`) || !strings.Contains(out, `"polls": 2`) {
		t.Fatalf("out = %s", out)
	}
}

func TestWaitTimeoutIsExitTwo(t *testing.T) {
	install(t, readyzFailing())
	code, out, _ := run(t, "wait", "--for", "readyz", "--kubeconfig", "synthetic", "--timeout", "3s", "--json")
	if code != 2 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(out, `"timeout"`) || !strings.Contains(out, `"outcome": "timeout"`) {
		t.Fatalf("out = %s", out)
	}
}

func TestWaitOnDeclaredDarkMakesNoClientAndNoRequest(t *testing.T) {
	for _, args := range [][]string{
		{"wait", "--for", "readyz", "--kubeconfig", "synthetic", "--expected", "dark", "--json"},
		{"wait", "--for", "readyz", "--config", writeConfig(t, twoClusters), "--cluster", "lab-one", "--json"},
	} {
		s := install(t, okObs(1, 1))
		code, out, _ := run(t, args...)
		if code != 3 || s.created != 0 || s.obs.calls != 0 {
			t.Fatalf("%v: code=%d created=%d calls=%d", args, code, s.created, s.obs.calls)
		}
		if !strings.Contains(out, `"wait_expected_dark"`) {
			t.Fatalf("out = %s", out)
		}
		if err := schematest.Status([]byte(out)); err != nil {
			t.Fatal(err)
		}
	}
	s := install(t, okObs(1, 1))
	code, _, _ := run(t, "wait", "--for", "readyz", "--config", writeConfig(t, twoClusters),
		"--cluster", "lab-one", "--expected", "light", "--kubeconfig", "synthetic")
	if code != 0 || s.created != 1 {
		t.Fatalf("--expected light must override the entry: code=%d created=%d", code, s.created)
	}
}

func TestWaitFailsFastOnAuth(t *testing.T) {
	s := install(t, okObs(1, 1))
	s.obs.obs = []report.Observation{{
		Apiserver: report.Apiserver{Result: report.Up},
		Failure:   exitcode.New(exitcode.AuthRejected, "the server rejected the credentials"),
	}}
	code, _, _ := run(t, "wait", "--for", "readyz", "--kubeconfig", "synthetic")
	if code != 4 || s.obs.calls != 1 || s.clk.sleeps != 0 {
		t.Fatalf("code=%d calls=%d sleeps=%d", code, s.obs.calls, s.clk.sleeps)
	}
}

func TestOutputNeverCarriesTheKubeconfigPath(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "private-dir-name", "kc")
	for _, args := range [][]string{
		{"status", "--kubeconfig", secret},
		{"status", "--kubeconfig", secret, "--json"},
		{"wait", "--for", "readyz", "--kubeconfig", secret, "--json"},
	} {
		var o, e bytes.Buffer
		code := Execute(args, Streams{Out: &o, Err: &e})
		if code != 3 {
			t.Errorf("%v: code = %d, want 3", args, code)
		}
		if strings.Contains(o.String()+e.String(), "private-dir-name") {
			t.Errorf("%v: path leaked: %q %q", args, o.String(), e.String())
		}
	}
}
