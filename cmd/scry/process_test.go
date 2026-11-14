// Process-level tests for the scry binary.
//
// The contract at this level is the process: exit code, stdout, stderr. The
// binary is built once and run against scripted loopback TLS fakes, so these
// tests see exactly what a caller sees — no in-process Execute, no shared
// state.
//
// This is the only file allowed to import os/exec (depguard exception); every
// other package keeps the ban, because Scry itself never runs another program.
package main_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/Hedronite/scry/internal/fakecluster"
)

var (
	binary   string
	buildErr error
)

// TestMain builds the real binary once. A failure here fails every test, which
// is the point: these tests are worthless if the binary is not the artefact
// under contract.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "scry-process-")
	if err != nil {
		panic(err)
	}
	binary = filepath.Join(dir, "scry")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		buildErr = fmt.Errorf("build scry: %w: %s", err, out)
	}
	cancel()
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// result is one process run.
type result struct {
	exit   int
	stdout string
	stderr string
	args   []string
	dir    string // the working directory the process ran in
	tmpdir string // the TMPDIR the process was given
}

func (r result) String() string {
	return "scry " + strings.Join(r.args, " ") +
		"\nexit: " + strconv.Itoa(r.exit) +
		"\nstdout: " + quote(r.stdout) +
		"\nstderr: " + quote(r.stderr)
}

func quote(s string) string {
	if s == "" {
		return `""`
	}
	return `"` + strings.ReplaceAll(s, "\n", `\n`) + `"`
}

// run executes the binary with a clean environment: no ambient KUBECONFIG, no
// home-directory kubeconfig, no inherited flags. Extra environment is opt-in.
func run(t *testing.T, env []string, args ...string) result {
	t.Helper()
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	dir := t.TempDir()
	tmpdir := t.TempDir()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"TMPDIR=" + tmpdir,
	}
	cmd.Env = append(cmd.Env, env...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("scry %s timed out: %v", strings.Join(args, " "), ctx.Err())
	}
	res := result{
		stdout: stdout.String(),
		stderr: stderr.String(),
		args:   append([]string(nil), args...),
		dir:    dir,
		tmpdir: tmpdir,
	}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		res.exit = 0
	case errors.As(err, &exitErr):
		res.exit = exitErr.ExitCode()
	default:
		t.Fatalf("run scry %s: %v", strings.Join(args, " "), err)
	}
	return res
}

func wantExit(t *testing.T, res result, want int) result {
	t.Helper()
	if res.exit != want {
		t.Fatalf("exit = %d, want %d\n%s", res.exit, want, res)
	}
	return res
}

// wantOneDocument decodes stdout as exactly one JSON document, with nothing
// after it: --json promises one document, not a stream.
func wantOneDocument(t *testing.T, res result) map[string]any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(res.stdout))
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, res)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		t.Fatalf("stdout carries more than one document: %v\n%s", err, res)
	}
	if doc["schema"] != "scry.status/v0" {
		t.Fatalf("schema = %v, want scry.status/v0\n%s", doc["schema"], res)
	}
	return doc
}

// wantNoSecrets proves the failure path carries no location or credential, in
// either stream, at any exit.
func wantNoSecrets(t *testing.T, res result, secrets ...string) result {
	t.Helper()
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		if strings.Contains(res.stdout, secret) {
			t.Fatalf("stdout leaks %q\n%s", secret, res)
		}
		if strings.Contains(res.stderr, secret) {
			t.Fatalf("stderr leaks %q\n%s", secret, res)
		}
	}
	return res
}

func wantSilent(t *testing.T, res result) result {
	t.Helper()
	if res.stdout != "" || res.stderr != "" {
		t.Fatalf("expected no output, got\n%s", res)
	}
	return res
}

// newFake starts a scripted cluster and closes it with the test.
func newFake(t *testing.T, opts fakecluster.Options) *fakecluster.Server {
	t.Helper()
	srv, err := fakecluster.New(opts)
	if err != nil {
		t.Fatalf("fake cluster: %v", err)
	}
	t.Cleanup(srv.Close)
	return srv
}

// kubeconfig writes a kubeconfig for srv and returns its path.
func kubeconfig(t *testing.T, srv *fakecluster.Server, mutate func(*clientcmdapi.Config)) string {
	t.Helper()
	path, err := srv.Kubeconfig(filepath.Join(t.TempDir(), "kubeconfig"), mutate)
	if err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	return path
}

// allowedPaths is the D4 wire contract as the process sees it.
var allowedPaths = map[string]bool{
	"/readyz":       true,
	"/livez":        true,
	"/version":      true,
	"/api/v1/nodes": true,
}

var nodePath = regexp.MustCompile(`^/api/v1/nodes/[^/]+$`)

// wantCleanWire asserts no watch, no non-GET, no unlisted path, and a fresh
// connection per request (Connection: close).
func wantCleanWire(t *testing.T, srv *fakecluster.Server) {
	t.Helper()
	for _, req := range srv.Requests() {
		if req.Method != "GET" {
			t.Fatalf("non-GET request on the wire: %s %s", req.Method, req.Path)
		}
		if strings.Contains(req.Query, "watch") {
			t.Fatalf("watch parameter on the wire: %s", req.Query)
		}
		if !allowedPaths[req.Path] && !nodePath.MatchString(req.Path) {
			t.Fatalf("request outside the allowlist: %s", req.Path)
		}
		if !req.Close {
			t.Fatalf("request did not ask to close the connection: %s %s", req.Method, req.Path)
		}
	}
}

func field(t *testing.T, doc map[string]any, path ...string) any {
	t.Helper()
	var cur any = doc
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("document path %v: %q is not an object", path, key)
		}
		cur, ok = m[key]
		if !ok {
			t.Fatalf("document path %v: missing %q", path, key)
		}
	}
	return cur
}

func wantField(t *testing.T, doc map[string]any, want any, path ...string) {
	t.Helper()
	if got := field(t, doc, path...); got != want {
		t.Fatalf("%v = %v, want %v", path, got, want)
	}
}

func reasons(doc map[string]any) []string {
	raw, _ := doc["reasons"].([]any)
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		if s, ok := r.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func wantReasonFirst(t *testing.T, doc map[string]any, want string) {
	t.Helper()
	got := reasons(doc)
	if len(got) == 0 || got[0] != want {
		t.Fatalf("reasons = %v, want %q first", got, want)
	}
}

// TestStatusGreen is the happy path, and the wire contract behind it.
func TestStatusGreen(t *testing.T) {
	srv := newFake(t, fakecluster.Options{})
	kc := kubeconfig(t, srv, nil)
	res := wantExit(t, run(t, nil, "status", "--json", "--kubeconfig", kc), 0)
	wantNoSecrets(t, res, fakecluster.DefaultToken, kc, srv.URL())

	doc := wantOneDocument(t, res)
	wantField(t, doc, true, "observed")
	wantField(t, doc, "GREEN", "severity")
	wantField(t, doc, "synthetic", "cluster", "alias")
	wantField(t, doc, "up", "apiserver", "result")
	wantField(t, doc, true, "readyz", "ok")

	if got := strings.Join(srv.Paths(), ","); got != "/readyz,/version,/api/v1/nodes" {
		t.Fatalf("wire paths = %s", got)
	}
	for _, req := range srv.Requests() {
		if req.Path == "/readyz" && req.Query != "verbose=true" {
			t.Fatalf("/readyz query = %q, want verbose=true", req.Query)
		}
	}
	if got := srv.Connections(); got != 3 {
		t.Fatalf("connections = %d, want 3: one fresh TLS connection per GET", got)
	}
	wantCleanWire(t, srv)
}

// TestStatusTable is the human surface: one alias and one verdict, on stdout.
func TestStatusTable(t *testing.T) {
	srv := newFake(t, fakecluster.Options{})
	kc := kubeconfig(t, srv, nil)
	res := wantExit(t, run(t, nil, "status", "--kubeconfig", kc), 0)
	if !strings.Contains(res.stdout, "synthetic") || !strings.Contains(res.stdout, "GREEN") {
		t.Fatalf("table lacks alias or verdict\n%s", res)
	}
	wantNoSecrets(t, res, fakecluster.DefaultToken, kc, srv.URL())
	wantCleanWire(t, srv)
}

func TestStatusYellow(t *testing.T) {
	srv := newFake(t, fakecluster.Options{
		Nodes: fakecluster.Reply{Body: fakecluster.NodeList(
			fakecluster.Node{Name: "node-a", Ready: "True", Reason: "KubeletReady"},
			fakecluster.Node{Name: "node-b", Ready: "False", Reason: "KubeletNotReady"},
		)},
	})
	kc := kubeconfig(t, srv, nil)
	doc := wantOneDocument(t, wantExit(t, run(t, nil, "status", "--json", "--kubeconfig", kc), 1))
	wantField(t, doc, true, "observed")
	wantField(t, doc, "YELLOW", "severity")
	wantCleanWire(t, srv)
}

// TestStatusYellowUnknownNode pins the additive K1 field: Ready=Unknown keeps
// the node out of the ready count, carries unknown:true, and keeps its reason.
func TestStatusYellowUnknownNode(t *testing.T) {
	srv := newFake(t, fakecluster.Options{
		Nodes: fakecluster.Reply{Body: fakecluster.NodeList(
			fakecluster.Node{Name: "node-a", Ready: "True", Reason: "KubeletReady"},
			fakecluster.Node{Name: "node-b", Ready: "Unknown", Reason: "NodeStatusUnknown"},
		)},
	})
	kc := kubeconfig(t, srv, nil)
	doc := wantOneDocument(t, wantExit(t, run(t, nil, "status", "--json", "--kubeconfig", kc), 1))
	wantField(t, doc, "YELLOW", "severity")
	wantField(t, doc, float64(1), "nodes", "ready")
	wantReasonFirst(t, doc, "node_not_ready")

	items, ok := field(t, doc, "nodes", "items").([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("nodes.items = %v", items)
	}
	unknown, ok := items[1].(map[string]any)
	if !ok {
		t.Fatalf("node item = %v", items[1])
	}
	if unknown["unknown"] != true || unknown["ready"] != false {
		t.Fatalf("unknown node = %v", unknown)
	}
	if unknown["reason"] != "NodeStatusUnknown" {
		t.Fatalf("reason = %v", unknown["reason"])
	}
	wantCleanWire(t, srv)
}

// TestStatusRedAllUnknown: a cluster where nothing is ready is RED, even when
// the reason is Unknown rather than False.
func TestStatusRedAllUnknown(t *testing.T) {
	srv := newFake(t, fakecluster.Options{
		Nodes: fakecluster.Reply{Body: fakecluster.NodeList(
			fakecluster.Node{Name: "node-a", Ready: "Unknown", Reason: "NodeStatusUnknown"},
		)},
	})
	kc := kubeconfig(t, srv, nil)
	doc := wantOneDocument(t, wantExit(t, run(t, nil, "status", "--json", "--kubeconfig", kc), 2))
	wantField(t, doc, "RED", "severity")
	wantField(t, doc, float64(0), "nodes", "ready")
	wantReasonFirst(t, doc, "node_not_ready")
	wantCleanWire(t, srv)
}

func TestStatusRedReadyz(t *testing.T) {
	srv := newFake(t, fakecluster.Options{
		Readyz: fakecluster.Reply{Status: 500, Body: "[-]etcd failed: unreachable\nreadyz check failed\n"},
	})
	kc := kubeconfig(t, srv, nil)
	doc := wantOneDocument(t, wantExit(t, run(t, nil, "status", "--json", "--kubeconfig", kc), 2))
	wantField(t, doc, "RED", "severity")
	wantReasonFirst(t, doc, "readyz_failed")
	if srv.Count("/livez") != 1 {
		t.Fatalf("expected one livez diagnostic, got %d", srv.Count("/livez"))
	}
	wantCleanWire(t, srv)
}

func TestStatusRedNoNodes(t *testing.T) {
	srv := newFake(t, fakecluster.Options{Nodes: fakecluster.Reply{Body: fakecluster.NodeList()}})
	kc := kubeconfig(t, srv, nil)
	doc := wantOneDocument(t, wantExit(t, run(t, nil, "status", "--json", "--kubeconfig", kc), 2))
	wantField(t, doc, "RED", "severity")
	wantReasonFirst(t, doc, "no_nodes")
	wantCleanWire(t, srv)
}

// TestStatusExpectedDarkDown is the D2 declaration path: a refused connect on a
// declared-dark apiserver is GREEN, not RED.
func TestStatusExpectedDarkDown(t *testing.T) {
	srv := newFake(t, fakecluster.Options{})
	kc := kubeconfig(t, srv, nil)
	srv.Close() // the connect refuses; refused is never retried

	doc := wantOneDocument(t, wantExit(t, run(t, nil,
		"status", "--json", "--kubeconfig", kc, "--expected", "dark"), 0))
	wantField(t, doc, true, "observed")
	wantField(t, doc, "GREEN", "severity")
	wantField(t, doc, "dark", "cluster", "expected")
	wantField(t, doc, "down", "apiserver", "result")
	wantReasonFirst(t, doc, "expected_dark")
}

// TestStatusLightDown is the same wire verdict without the declaration: RED.
func TestStatusLightDown(t *testing.T) {
	srv := newFake(t, fakecluster.Options{})
	kc := kubeconfig(t, srv, nil)
	srv.Close()

	doc := wantOneDocument(t, wantExit(t, run(t, nil, "status", "--json", "--kubeconfig", kc), 2))
	wantField(t, doc, true, "observed")
	wantField(t, doc, "RED", "severity")
	wantField(t, doc, "down", "apiserver", "result")
	wantReasonFirst(t, doc, "apiserver_down")
}

// TestStatusDarkButAnswering: a declared-dark cluster that answers is evaluated
// normally, with no expected_dark reason.
func TestStatusDarkButAnswering(t *testing.T) {
	srv := newFake(t, fakecluster.Options{})
	kc := kubeconfig(t, srv, nil)
	doc := wantOneDocument(t, wantExit(t, run(t, nil,
		"status", "--json", "--kubeconfig", kc, "--expected", "dark"), 0))
	wantField(t, doc, "GREEN", "severity")
	for _, reason := range reasons(doc) {
		if reason == "expected_dark" {
			t.Fatalf("expected_dark on an answering apiserver: %v", reasons(doc))
		}
	}
}

// TestConfigErrors is the exit-3 family: every one ends before a socket opens.
func TestConfigErrors(t *testing.T) {
	cases := []struct {
		name  string
		label string
		args  func(t *testing.T, srv *fakecluster.Server) ([]string, []string)
	}{
		{
			name:  "no kubeconfig",
			label: "kubeconfig_missing",
			args: func(*testing.T, *fakecluster.Server) ([]string, []string) {
				return []string{"status", "--json"}, nil
			},
		},
		{
			name:  "missing file",
			label: "kubeconfig_missing",
			args: func(t *testing.T, _ *fakecluster.Server) ([]string, []string) {
				missing := filepath.Join(t.TempDir(), "absent")
				return []string{"status", "--json", "--kubeconfig", missing}, []string{missing}
			},
		},
		{
			name:  "kubeconfig is a directory",
			label: "kubeconfig_invalid",
			args: func(t *testing.T, _ *fakecluster.Server) ([]string, []string) {
				dir := t.TempDir()
				return []string{"status", "--json", "--kubeconfig", dir}, []string{dir}
			},
		},
		{
			name:  "malformed kubeconfig",
			label: "kubeconfig_invalid",
			args: func(t *testing.T, _ *fakecluster.Server) ([]string, []string) {
				path := filepath.Join(t.TempDir(), "kubeconfig")
				if err := os.WriteFile(path, []byte("clusters: [not: yaml"), 0o600); err != nil {
					t.Fatalf("write: %v", err)
				}
				return []string{"status", "--json", "--kubeconfig", path}, nil
			},
		},
		{
			name:  "exec auth",
			label: "exec_auth_refused",
			args: func(t *testing.T, srv *fakecluster.Server) ([]string, []string) {
				marker := filepath.Join(t.TempDir(), "exec-ran")
				kc := kubeconfig(t, srv, func(cfg *clientcmdapi.Config) {
					cfg.AuthInfos["user"] = &clientcmdapi.AuthInfo{
						Exec: &clientcmdapi.ExecConfig{
							APIVersion: "client.authentication.k8s.io/v1",
							Command:    "/bin/sh",
							Args:       []string{"-c", "touch " + marker},
						},
					}
				})
				t.Cleanup(func() {
					if _, err := os.Stat(marker); !os.IsNotExist(err) {
						t.Errorf("exec plugin ran")
					}
				})
				return []string{"status", "--json", "--kubeconfig", kc}, nil
			},
		},
		{
			name:  "auth provider",
			label: "auth_provider_refused",
			args: func(t *testing.T, srv *fakecluster.Server) ([]string, []string) {
				kc := kubeconfig(t, srv, func(cfg *clientcmdapi.Config) {
					cfg.AuthInfos["user"] = &clientcmdapi.AuthInfo{
						AuthProvider: &clientcmdapi.AuthProviderConfig{Name: "oidc"},
					}
				})
				return []string{"status", "--json", "--kubeconfig", kc}, nil
			},
		},
		{
			name:  "insecure tls",
			label: "insecure_tls_refused",
			args: func(t *testing.T, srv *fakecluster.Server) ([]string, []string) {
				kc := kubeconfig(t, srv, func(cfg *clientcmdapi.Config) {
					cfg.Clusters["cluster"].InsecureSkipTLSVerify = true
				})
				return []string{"status", "--json", "--kubeconfig", kc}, nil
			},
		},
		{
			name:  "plaintext server",
			label: "kubeconfig_invalid",
			args: func(t *testing.T, srv *fakecluster.Server) ([]string, []string) {
				kc := kubeconfig(t, srv, func(cfg *clientcmdapi.Config) {
					cfg.Clusters["cluster"].Server = "http://127.0.0.1:1"
				})
				return []string{"status", "--json", "--kubeconfig", kc}, nil
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newFake(t, fakecluster.Options{})
			args, secrets := tc.args(t, srv)
			res := wantExit(t, run(t, nil, args...), 3)
			doc := wantOneDocument(t, res)
			wantField(t, doc, false, "observed")
			wantField(t, doc, tc.label, "error", "code")
			wantField(t, doc, "unknown", "apiserver", "result")
			if _, ok := doc["severity"]; ok {
				t.Fatalf("unobserved document carries a severity: %v", doc["severity"])
			}
			if got := srv.Requests(); len(got) != 0 {
				t.Fatalf("a refused configuration opened %d request(s)", len(got))
			}
			wantNoSecrets(t, res, append(secrets, fakecluster.DefaultToken, srv.URL())...)
		})
	}
}

func TestContextMissListsNamesOnStderrOnly(t *testing.T) {
	srv := newFake(t, fakecluster.Options{})
	kc := kubeconfig(t, srv, func(cfg *clientcmdapi.Config) {
		cfg.Contexts["second-context"] = &clientcmdapi.Context{Cluster: "cluster", AuthInfo: "user"}
	})
	res := wantExit(t, run(t, nil, "status", "--json", "--kubeconfig", kc, "--context", "absent"), 3)
	doc := wantOneDocument(t, res)
	wantField(t, doc, false, "observed")
	wantField(t, doc, "unnamed", "cluster", "alias")
	if !strings.Contains(res.stderr, "second-context") && !strings.Contains(res.stderr, "synthetic") {
		t.Fatalf("stderr does not list the available contexts\n%s", res)
	}
	if strings.Contains(res.stdout, "second-context") {
		t.Fatalf("context names leaked into the document\n%s", res)
	}
	if got := srv.Requests(); len(got) != 0 {
		t.Fatalf("a context miss opened %d request(s)", len(got))
	}
	wantNoSecrets(t, res, fakecluster.DefaultToken, kc, srv.URL())

	withFlag := wantExit(t, run(t, nil, "status", "--json", "--kubeconfig", kc, "--context", "absent", "--cluster", "edge"), 3)
	wantField(t, wantOneDocument(t, withFlag), "edge", "cluster", "alias")
}

func TestAuthExitFour(t *testing.T) {
	srv := newFake(t, fakecluster.Options{Handle: func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == fakecluster.ReadyzPath {
			w.WriteHeader(401)
			return true
		}
		return false
	}})
	kc := kubeconfig(t, srv, nil)
	res := wantExit(t, run(t, nil, "status", "--json", "--kubeconfig", kc), 4)
	doc := wantOneDocument(t, res)
	wantField(t, doc, false, "observed")
	wantField(t, doc, "up", "apiserver", "result")
	wantField(t, doc, "auth_rejected", "error", "code")
	wantCleanWire(t, srv)
	wantNoSecrets(t, res, fakecluster.DefaultToken, kc, srv.URL())
}

func TestTLSExitFive(t *testing.T) {
	stranger := newFake(t, fakecluster.Options{})
	srv := newFake(t, fakecluster.Options{})
	kc := kubeconfig(t, srv, func(cfg *clientcmdapi.Config) {
		cfg.Clusters["cluster"].CertificateAuthorityData = stranger.CAPEM()
	})
	res := wantExit(t, run(t, nil, "status", "--json", "--kubeconfig", kc), 5)
	doc := wantOneDocument(t, res)
	wantField(t, doc, false, "observed")
	wantField(t, doc, "up", "apiserver", "result")
	wantField(t, doc, "tls_verify_failed", "error", "code")
	wantNoSecrets(t, res, fakecluster.DefaultToken, kc, srv.URL())
}

// TestTransportExitSix uses a config entry's per-request timeout so the silent
// peer is classified in a fraction of a second.
func TestTransportExitSix(t *testing.T) {
	srv := newFake(t, fakecluster.Options{Readyz: fakecluster.Reply{Hang: true}})
	kc := kubeconfig(t, srv, nil)
	cfg := writeConfig(t, "synthetic", kc, "light", "200ms")
	res := wantExit(t, run(t, nil, "status", "--json", "--config", cfg, "--cluster", "synthetic"), 6)
	doc := wantOneDocument(t, res)
	wantField(t, doc, false, "observed")
	wantField(t, doc, "unknown", "apiserver", "result")
	wantField(t, doc, "http_unresponsive", "error", "code")
	if got := srv.Count("/readyz"); got != 2 {
		t.Fatalf("silent peer saw %d readyz request(s), want 2 (one retry)", got)
	}
	wantCleanWire(t, srv)
}

func writeConfig(t *testing.T, alias, kubeconfigPath, expected, timeout string) string {
	t.Helper()
	body := fmt.Sprintf(`schema = "scry.config/v0"

[[cluster]]
alias = %q
kubeconfig = %q
expected = %q
timeout = %q
`, alias, kubeconfigPath, expected, timeout)
	path := filepath.Join(t.TempDir(), "scry.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestConfigFileErrors(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"malformed toml", "not toml at all ["},
		{"wrong schema", "schema = \"scry.config/v1\"\n"},
		{"unknown field", "schema = \"scry.config/v0\"\n[[cluster]]\nalias = \"a\"\nextra = 1\n"},
		{"url alias", "schema = \"scry.config/v0\"\n[[cluster]]\nalias = \"https://x\"\n"},
		{"empty file", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "scry.toml")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}
			res := wantExit(t, run(t, nil, "status", "--json", "--config", path), 3)
			doc := wantOneDocument(t, res)
			wantField(t, doc, false, "observed")
			if got := field(t, doc, "error", "code"); got != "config_invalid" {
				t.Fatalf("error.code = %v, want config_invalid\n%s", got, res)
			}
			wantNoSecrets(t, res, path)
		})
	}

	t.Run("config is a directory", func(t *testing.T) {
		res := wantExit(t, run(t, nil, "status", "--json", "--config", t.TempDir()), 3)
		wantField(t, wantOneDocument(t, res), false, "observed")
	})
}

// TestClusterSelection pins the config/flag precedence: one entry may be
// implicit, several require --cluster, and a miss lists the aliases.
func TestClusterSelection(t *testing.T) {
	t.Run("single entry is implicit", func(t *testing.T) {
		srv := newFake(t, fakecluster.Options{})
		cfg := writeConfig(t, "synthetic", kubeconfig(t, srv, nil), "light", "5s")
		doc := wantOneDocument(t, wantExit(t, run(t, nil, "status", "--json", "--config", cfg), 0))
		wantField(t, doc, "synthetic", "cluster", "alias")
	})

	t.Run("several entries need a cluster", func(t *testing.T) {
		first := newFake(t, fakecluster.Options{})
		second := newFake(t, fakecluster.Options{})
		kcFirst := kubeconfig(t, first, nil)
		kcSecond := kubeconfig(t, second, nil)
		cfg := filepath.Join(t.TempDir(), "scry.toml")
		body := fmt.Sprintf(`schema = "scry.config/v0"

[[cluster]]
alias = "lab"
kubeconfig = %q
expected = "light"
timeout = "5s"

[[cluster]]
alias = "edge"
kubeconfig = %q
expected = "light"
timeout = "5s"
`, kcFirst, kcSecond)
		if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		res := wantExit(t, run(t, nil, "status", "--json", "--config", cfg), 3)
		doc := wantOneDocument(t, res)
		wantField(t, doc, false, "observed")
		wantField(t, doc, "unnamed", "cluster", "alias")
		if !strings.Contains(res.stderr, "lab") || !strings.Contains(res.stderr, "edge") {
			t.Fatalf("stderr does not list the aliases\n%s", res)
		}
		if got := len(first.Requests()) + len(second.Requests()); got != 0 {
			t.Fatalf("an ambiguous config opened %d request(s)", got)
		}
	})

	t.Run("unknown cluster", func(t *testing.T) {
		srv := newFake(t, fakecluster.Options{})
		cfg := writeConfig(t, "synthetic", kubeconfig(t, srv, nil), "light", "5s")
		res := wantExit(t, run(t, nil, "status", "--json", "--config", cfg, "--cluster", "nope"), 3)
		doc := wantOneDocument(t, res)
		wantField(t, doc, false, "observed")
		wantField(t, doc, "nope", "cluster", "alias")
	})
}

func TestKubeconfigSourceRules(t *testing.T) {
	srv := newFake(t, fakecluster.Options{})
	kc := kubeconfig(t, srv, nil)
	env := []string{"KUBECONFIG=" + kc}

	t.Run("env needs the opt-in", func(t *testing.T) {
		res := wantExit(t, run(t, env, "status", "--json"), 3)
		wantField(t, wantOneDocument(t, res), "kubeconfig_missing", "error", "code")
		if got := srv.Requests(); len(got) != 0 {
			t.Fatalf("ignored KUBECONFIG opened %d request(s)", len(got))
		}
	})

	t.Run("opt-in uses env", func(t *testing.T) {
		wantExit(t, run(t, env, "status", "--json", "--allow-env-kubeconfig"), 0)
	})

	t.Run("explicit wins over env", func(t *testing.T) {
		explicit := kubeconfig(t, srv, nil)
		badEnv := []string{"KUBECONFIG=" + filepath.Join(t.TempDir(), "absent")}
		wantExit(t, run(t, badEnv, "status", "--json", "--allow-env-kubeconfig", "--kubeconfig", explicit), 0)
	})
}

func TestWaitReady(t *testing.T) {
	srv := newFake(t, fakecluster.Options{})
	kc := kubeconfig(t, srv, nil)
	doc := wantOneDocument(t, wantExit(t, run(t, nil,
		"wait", "--json", "--for", "readyz", "--timeout", "10s", "--kubeconfig", kc), 0))
	wantField(t, doc, "ready", "wait", "outcome")
	wantField(t, doc, "GREEN", "severity")
	wantCleanWire(t, srv)
}

// TestWaitDegradedReady exits 1: readyz passes but a node is not ready.
func TestWaitDegradedReady(t *testing.T) {
	srv := newFake(t, fakecluster.Options{
		Nodes: fakecluster.Reply{Body: fakecluster.NodeList(
			fakecluster.Node{Name: "node-a", Ready: "True", Reason: "KubeletReady"},
			fakecluster.Node{Name: "node-b", Ready: "False", Reason: "KubeletNotReady"},
		)},
	})
	kc := kubeconfig(t, srv, nil)
	doc := wantOneDocument(t, wantExit(t, run(t, nil,
		"wait", "--json", "--for", "readyz", "--timeout", "10s", "--kubeconfig", kc), 1))
	wantField(t, doc, "ready", "wait", "outcome")
	wantField(t, doc, "YELLOW", "severity")
}

// TestWaitTimeout pins exit 2 with the timeout reason first, on the last poll.
func TestWaitTimeout(t *testing.T) {
	srv := newFake(t, fakecluster.Options{
		Readyz: fakecluster.Reply{Status: 500, Body: "[-]etcd failed\nreadyz check failed\n"},
	})
	kc := kubeconfig(t, srv, nil)
	doc := wantOneDocument(t, wantExit(t, run(t, nil,
		"wait", "--json", "--for", "readyz", "--timeout", "700ms", "--kubeconfig", kc), 2))
	wantField(t, doc, "timeout", "wait", "outcome")
	wantReasonFirst(t, doc, "timeout")
	if polls, ok := field(t, doc, "wait", "polls").(float64); !ok || polls < 2 {
		t.Fatalf("wait.polls = %v, want at least 2", field(t, doc, "wait", "polls"))
	}
	wantCleanWire(t, srv)
}

// TestWaitDeclaredDarkMakesNoRequests is decision 5: dark is a declaration, not
// a probe.
func TestWaitDeclaredDarkMakesNoRequests(t *testing.T) {
	srv := newFake(t, fakecluster.Options{})
	kc := kubeconfig(t, srv, nil)
	res := wantExit(t, run(t, nil,
		"wait", "--json", "--for", "readyz", "--timeout", "2s", "--kubeconfig", kc, "--expected", "dark"), 3)
	doc := wantOneDocument(t, res)
	wantField(t, doc, false, "observed")
	wantField(t, doc, "wait_expected_dark", "error", "code")
	if got := srv.Requests(); len(got) != 0 {
		t.Fatalf("declared-dark wait opened %d request(s)", len(got))
	}
	wantNoSecrets(t, res, fakecluster.DefaultToken, kc, srv.URL())
}

// TestWaitAuthFailsFast: a 401 cannot heal, so wait must not burn the timeout.
func TestWaitAuthFailsFast(t *testing.T) {
	srv := newFake(t, fakecluster.Options{Handle: func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == fakecluster.ReadyzPath {
			w.WriteHeader(401)
			return true
		}
		return false
	}})
	kc := kubeconfig(t, srv, nil)
	start := time.Now()
	res := wantExit(t, run(t, nil,
		"wait", "--json", "--for", "readyz", "--timeout", "20s", "--kubeconfig", kc), 4)
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("wait burned %v on an unrecoverable auth failure", elapsed)
	}
	wantField(t, wantOneDocument(t, res), "auth_rejected", "error", "code")
	if got := srv.Count("/readyz"); got != 1 {
		t.Fatalf("auth failure polled %d time(s), want 1", got)
	}
}

// TestWaitKeepsPollingOnTransport: a silent peer can heal, so it is retried
// until the timeout, and the timeout is still exit 2.
func TestWaitKeepsPollingOnTransport(t *testing.T) {
	srv := newFake(t, fakecluster.Options{Readyz: fakecluster.Reply{Hang: true}})
	kc := kubeconfig(t, srv, nil)
	cfg := writeConfig(t, "synthetic", kc, "light", "150ms")
	doc := wantOneDocument(t, wantExit(t, run(t, nil,
		"wait", "--json", "--for", "readyz", "--timeout", "1s", "--config", cfg, "--cluster", "synthetic"), 2))
	wantField(t, doc, "timeout", "wait", "outcome")
	if got := srv.Count("/readyz"); got < 2 {
		t.Fatalf("transport failure polled %d time(s), want at least 2", got)
	}
}

func TestQuietSilences(t *testing.T) {
	healthy := newFake(t, fakecluster.Options{})
	kcHealthy := kubeconfig(t, healthy, nil)
	wantSilent(t, wantExit(t, run(t, nil, "status", "--quiet", "--kubeconfig", kcHealthy), 0))

	red := newFake(t, fakecluster.Options{Readyz: fakecluster.Reply{Status: 500}})
	kcRed := kubeconfig(t, red, nil)
	wantSilent(t, wantExit(t, run(t, nil, "status", "--quiet", "--kubeconfig", kcRed), 2))

	wantSilent(t, wantExit(t, run(t, nil, "status", "--quiet"), 3))

	res := wantExit(t, run(t, nil, "status", "--quiet", "--nonsense"), 8)
	if res.stdout != "" || res.stderr == "" {
		t.Fatalf("usage error under --quiet should print one line to stderr\n%s", res)
	}
}

// TestUsageExits is the flag surface: every rejection is exit 8, with one
// stderr line and an empty stdout, and none of them needs a cluster.
func TestUsageExits(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"unknown flag", []string{"status", "--nonsense"}},
		{"unknown command", []string{"frobnicate"}},
		{"json and quiet", []string{"status", "--json", "--quiet"}},
		{"bad expected", []string{"status", "--expected", "sideways"}},
		{"bad cluster alias", []string{"status", "--cluster", "Bad_Alias"}},
		{"cluster alias too long", []string{"status", "--cluster", strings.Repeat("a", 33)}},
		{"missing wait predicate", []string{"wait", "--kubeconfig", "/nonexistent"}},
		{"bad wait predicate", []string{"wait", "--for", "liveness", "--kubeconfig", "/nonexistent"}},
		{"zero timeout", []string{"wait", "--timeout", "0s", "--kubeconfig", "/nonexistent"}},
		{"status takes no args", []string{"status", "extra"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := wantExit(t, run(t, nil, tc.args...), 8)
			if res.stdout != "" {
				t.Fatalf("usage error wrote to stdout\n%s", res)
			}
			if !strings.Contains(res.stderr, "try 'scry --help'") {
				t.Fatalf("usage error lacks the help hint\n%s", res)
			}
		})
	}
}

func TestVersion(t *testing.T) {
	res := wantExit(t, run(t, nil, "version"), 0)
	if !strings.HasPrefix(res.stdout, "scry ") {
		t.Fatalf("version output = %q", res.stdout)
	}
	if res.stderr != "" {
		t.Fatalf("version wrote to stderr: %q", res.stderr)
	}
	doc := wantExit(t, run(t, nil, "version", "--json"), 0)
	var vd map[string]any
	if err := json.Unmarshal([]byte(doc.stdout), &vd); err != nil {
		t.Fatalf("version --json: %v\n%s", err, doc)
	}
	if vd["schema"] != "scry.version/v0" {
		t.Fatalf("schema = %v", vd["schema"])
	}
}

func TestHelp(t *testing.T) {
	res := wantExit(t, run(t, nil, "--help"), 0)
	for _, want := range []string{"status", "wait", "version"} {
		if !strings.Contains(res.stdout, want) {
			t.Fatalf("help does not list %q\n%s", want, res)
		}
	}
	for _, cmd := range []string{"status", "wait"} {
		res := wantExit(t, run(t, nil, cmd, "--help"), 0)
		if !strings.Contains(res.stdout, "Usage:") {
			t.Fatalf("%s --help lacks a usage block\n%s", cmd, res)
		}
	}
}

// --- K2: status --out, the Vigil file export --------------------------------

// outPath returns a path inside a fresh directory the test owns.
func outPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "status.json")
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return body
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// wantNoLeftovers asserts the target directory holds only the exported file:
// the temp file must not survive a success.
func wantNoLeftovers(t *testing.T, dir, target string) {
	t.Helper()
	names := dirNames(t, dir)
	if len(names) != 1 || names[0] != target {
		t.Fatalf("directory %s holds %v, want only %q", dir, names, target)
	}
}

// wantOutDocument asserts the exported file is the full document, with the
// mode the contract fixes.
func wantOutDocument(t *testing.T, path string, mode os.FileMode) map[string]any {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if info.Mode().Perm() != mode {
		t.Fatalf("%s mode = %v, want %v", path, info.Mode().Perm(), mode)
	}
	var doc map[string]any
	if err := json.Unmarshal(readFile(t, path), &doc); err != nil {
		t.Fatalf("exported file is not one JSON document: %v", err)
	}
	if doc["schema"] != "scry.status/v0" {
		t.Fatalf("exported schema = %v", doc["schema"])
	}
	return doc
}

// TestOutWrittenOnObservedExits is R4's observed half: the file appears when a
// verdict was reached, and it is byte-identical to the --json document.
func TestOutWrittenOnObservedExits(t *testing.T) {
	cases := []struct {
		name     string
		opts     fakecluster.Options
		severity string
		exit     int
	}{
		{"green", fakecluster.Options{}, "GREEN", 0},
		{
			"yellow",
			fakecluster.Options{Nodes: fakecluster.Reply{Body: fakecluster.NodeList(
				fakecluster.Node{Name: "node-a", Ready: "True", Reason: "KubeletReady"},
				fakecluster.Node{Name: "node-b", Ready: "False", Reason: "KubeletNotReady"},
			)}},
			"YELLOW", 1,
		},
		{"red", fakecluster.Options{Readyz: fakecluster.Reply{Status: 500}}, "RED", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newFake(t, tc.opts)
			kc := kubeconfig(t, srv, nil)
			out := outPath(t)
			res := wantExit(t, run(t, nil, "status", "--json", "--out", out, "--kubeconfig", kc), tc.exit)

			doc := wantOutDocument(t, out, 0o644)
			wantField(t, doc, true, "observed")
			wantField(t, doc, tc.severity, "severity")
			if string(readFile(t, out)) != res.stdout {
				t.Fatalf("exported file differs from --json stdout\nfile: %s\nstdout: %s", quote(string(readFile(t, out))), quote(res.stdout))
			}
			wantNoLeftovers(t, filepath.Dir(out), filepath.Base(out))
			wantCleanWire(t, srv)
		})
	}
}

// TestOutWrittenOnUnobservedExits is R4's unobserved half: the document is
// still written, without a severity.
func TestOutWrittenOnUnobservedExits(t *testing.T) {
	t.Run("auth", func(t *testing.T) {
		srv := newFake(t, fakecluster.Options{Handle: func(w http.ResponseWriter, r *http.Request) bool {
			if r.URL.Path == fakecluster.ReadyzPath {
				w.WriteHeader(401)
				return true
			}
			return false
		}})
		kc := kubeconfig(t, srv, nil)
		out := outPath(t)
		wantExit(t, run(t, nil, "status", "--json", "--out", out, "--kubeconfig", kc), 4)
		doc := wantOutDocument(t, out, 0o644)
		wantField(t, doc, false, "observed")
		wantField(t, doc, "auth_rejected", "error", "code")
		if _, ok := doc["severity"]; ok {
			t.Fatalf("unobserved export carries a severity: %v", doc["severity"])
		}
		wantNoLeftovers(t, filepath.Dir(out), filepath.Base(out))
	})

	t.Run("tls", func(t *testing.T) {
		stranger := newFake(t, fakecluster.Options{})
		srv := newFake(t, fakecluster.Options{})
		kc := kubeconfig(t, srv, func(cfg *clientcmdapi.Config) {
			cfg.Clusters["cluster"].CertificateAuthorityData = stranger.CAPEM()
		})
		out := outPath(t)
		wantExit(t, run(t, nil, "status", "--json", "--out", out, "--kubeconfig", kc), 5)
		doc := wantOutDocument(t, out, 0o644)
		wantField(t, doc, false, "observed")
		wantField(t, doc, "up", "apiserver", "result")
		wantField(t, doc, "tls_verify_failed", "error", "code")
		wantNoLeftovers(t, filepath.Dir(out), filepath.Base(out))
	})

	t.Run("transport", func(t *testing.T) {
		srv := newFake(t, fakecluster.Options{Readyz: fakecluster.Reply{Hang: true}})
		kc := kubeconfig(t, srv, nil)
		cfg := writeConfig(t, "synthetic", kc, "light", "200ms")
		out := outPath(t)
		wantExit(t, run(t, nil, "status", "--json", "--out", out, "--config", cfg, "--cluster", "synthetic"), 6)
		doc := wantOutDocument(t, out, 0o644)
		wantField(t, doc, false, "observed")
		wantField(t, doc, "http_unresponsive", "error", "code")
		wantNoLeftovers(t, filepath.Dir(out), filepath.Base(out))
	})
}

// TestOutUntouchedOnPreNetworkRefusal is R4: exit 3 never writes, and an older
// file keeps its bytes.
func TestOutUntouchedOnPreNetworkRefusal(t *testing.T) {
	old := []byte(`{"schema":"scry.status/v0","sentinel":"unchanged"}`)

	t.Run("existing file", func(t *testing.T) {
		out := outPath(t)
		if err := os.WriteFile(out, old, 0o644); err != nil {
			t.Fatalf("seed: %v", err)
		}
		wantExit(t, run(t, nil, "status", "--json", "--out", out), 3)
		if got := readFile(t, out); !bytes.Equal(got, old) {
			t.Fatalf("refusal rewrote the export: %q", got)
		}
		wantNoLeftovers(t, filepath.Dir(out), filepath.Base(out))
	})

	t.Run("no file", func(t *testing.T) {
		out := outPath(t)
		wantExit(t, run(t, nil, "status", "--json", "--out", out), 3)
		if names := dirNames(t, filepath.Dir(out)); len(names) != 0 {
			t.Fatalf("refusal created %v", names)
		}
	})
}

// TestOutWriteFailure is R5, and the deterministic proof that the temp file is
// created in the target directory: with the directory unwritable and TMPDIR
// writable, a TMPDIR-based writer would succeed.
func TestOutWriteFailure(t *testing.T) {
	srv := newFake(t, fakecluster.Options{})
	kc := kubeconfig(t, srv, nil)

	dir := t.TempDir()
	out := filepath.Join(dir, "status.json")
	if err := os.WriteFile(out, []byte("old, untouched"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	res := wantExit(t, run(t, nil, "status", "--json", "--out", out, "--kubeconfig", kc), 3)
	if res.stderr != "scry: cannot write the --out file\n" {
		t.Fatalf("stderr = %q, want exactly the frozen message", res.stderr)
	}
	doc := wantOneDocument(t, res)
	wantField(t, doc, false, "observed")
	wantField(t, doc, "unknown", "apiserver", "result")
	wantField(t, doc, "out_write_failed", "error", "code")
	if _, ok := doc["severity"]; ok {
		t.Fatalf("write failure carries a severity: %v", doc["severity"])
	}
	if got := string(readFile(t, out)); got != "old, untouched" {
		t.Fatalf("the old export was modified: %q", got)
	}
	if names := dirNames(t, dir); len(names) != 1 || names[0] != "status.json" {
		t.Fatalf("write failure left %v", names)
	}
	if names := dirNames(t, res.tmpdir); len(names) != 0 {
		t.Fatalf("write failure used TMPDIR for the temp file: %v", names)
	}
}

// TestOutRequiresAPath pins the two usage rejections.
func TestOutRequiresAPath(t *testing.T) {
	for _, value := range []string{"", "-"} {
		res := wantExit(t, run(t, nil, "status", "--out", value), 8)
		if res.stdout != "" {
			t.Fatalf("usage error wrote to stdout\n%s", res)
		}
		if names := dirNames(t, res.dir); len(names) != 0 {
			t.Fatalf("--out %q wrote %v", value, names)
		}
	}
}

// TestOutMustNotAliasAnInput: exporting over the kubeconfig or the config file
// would destroy the input, so it is a usage error.
func TestOutMustNotAliasAnInput(t *testing.T) {
	t.Run("kubeconfig", func(t *testing.T) {
		srv := newFake(t, fakecluster.Options{})
		kc := kubeconfig(t, srv, nil)
		before := readFile(t, kc)
		res := wantExit(t, run(t, nil, "status", "--out", kc, "--kubeconfig", kc), 8)
		if res.stdout != "" {
			t.Fatalf("usage error wrote to stdout\n%s", res)
		}
		if got := readFile(t, kc); !bytes.Equal(got, before) {
			t.Fatalf("the kubeconfig was overwritten")
		}
	})

	t.Run("config", func(t *testing.T) {
		srv := newFake(t, fakecluster.Options{})
		cfg := writeConfig(t, "synthetic", kubeconfig(t, srv, nil), "light", "5s")
		before := readFile(t, cfg)
		wantExit(t, run(t, nil, "status", "--out", cfg, "--config", cfg, "--cluster", "synthetic"), 8)
		if got := readFile(t, cfg); !bytes.Equal(got, before) {
			t.Fatalf("the config file was overwritten")
		}
	})
}

// TestOutRejectedOnWait is R1.
func TestOutRejectedOnWait(t *testing.T) {
	srv := newFake(t, fakecluster.Options{})
	kc := kubeconfig(t, srv, nil)
	out := outPath(t)
	wantExit(t, run(t, nil, "wait", "--for", "readyz", "--out", out, "--kubeconfig", kc), 8)
	if names := dirNames(t, filepath.Dir(out)); len(names) != 0 {
		t.Fatalf("wait --out wrote %v", names)
	}
	if got := srv.Requests(); len(got) != 0 {
		t.Fatalf("wait --out opened %d request(s)", len(got))
	}
}

// TestOutQuietStillWrites: --quiet silences the streams, not the export.
func TestOutQuietStillWrites(t *testing.T) {
	srv := newFake(t, fakecluster.Options{})
	kc := kubeconfig(t, srv, nil)
	out := outPath(t)
	wantSilent(t, wantExit(t, run(t, nil, "status", "--quiet", "--out", out, "--kubeconfig", kc), 0))
	doc := wantOutDocument(t, out, 0o644)
	wantField(t, doc, true, "observed")
	wantField(t, doc, "GREEN", "severity")
}

// TestOutReplaceIsAtomic rewrites the export ten times while a reader loops over
// it: every read must be a complete document, never empty and never partial.
func TestOutReplaceIsAtomic(t *testing.T) {
	srv := newFake(t, fakecluster.Options{})
	kc := kubeconfig(t, srv, nil)
	out := outPath(t)

	wantExit(t, run(t, nil, "status", "--json", "--out", out, "--kubeconfig", kc), 0)

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		badRead []string
		reads   int
	)
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			body, err := os.ReadFile(out)
			if err != nil {
				continue
			}
			mu.Lock()
			reads++
			switch {
			case len(bytes.TrimSpace(body)) == 0:
				badRead = append(badRead, "empty read")
			case !json.Valid(body):
				badRead = append(badRead, "partial read")
			}
			mu.Unlock()
		}
	}()

	for i := 0; i < 10; i++ {
		wantExit(t, run(t, nil, "status", "--json", "--out", out, "--kubeconfig", kc), 0)
	}
	close(stop)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if reads == 0 {
		t.Fatal("the reader never observed the export")
	}
	if len(badRead) > 0 {
		t.Fatalf("non-atomic replace: %d of %d reads were %v", len(badRead), reads, badRead)
	}
}
