package kube

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hedronite/scry/internal/exitcode"
	"github.com/Hedronite/scry/internal/report"
)

const (
	syntheticVersion  = `{"major":"1","minor":"34","gitVersion":"v1.34.0+h3s","platform":"darwin/arm64"}`
	syntheticNodeList = `{"kind":"NodeList","apiVersion":"v1","items":[` +
		`{"metadata":{"name":"node-b"},"status":{"conditions":[{"type":"Ready","status":"False","reason":"RuntimeNotReady"}]}},` +
		`{"metadata":{"name":"node-a"},"status":{"conditions":[{"type":"Ready","status":"True","reason":"KubeletReady"},{"type":"NetworkUnavailable","status":"False"}]}}` +
		`]}`
	syntheticForbidden = `{"kind":"Status","apiVersion":"v1","status":"Failure","message":"nodes is forbidden","reason":"Forbidden","code":403}`
	verboseFailed      = "[+]ping ok\n[+]log ok\n[-]etcd failed: reason withheld\nreadyz check failed\n"
)

// fakeAPI is a synthetic apiserver. Every request is recorded so a test can
// assert which paths were touched and that no extra call was made.
type fakeAPI struct {
	readyzStatus int
	readyzBody   string
	livezStatus  int
	livezBody    string
	versionBody  string
	nodesStatus  int
	nodesBody    string

	mu    sync.Mutex
	paths []string
	auth  []string
}

func newFakeAPI() *fakeAPI {
	return &fakeAPI{
		readyzStatus: http.StatusOK,
		readyzBody:   "ok\n",
		livezStatus:  http.StatusOK,
		livezBody:    "ok\n",
		versionBody:  syntheticVersion,
		nodesStatus:  http.StatusOK,
		nodesBody:    syntheticNodeList,
	}
}

// readyzHandler is the minimal synthetic apiserver some tests need.
func readyzHandler(body string) http.Handler {
	api := newFakeAPI()
	api.readyzBody = body
	return api
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.paths = append(f.paths, r.URL.Path)
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.mu.Unlock()

	f.mu.Lock()
	readyzStatus, readyzBody := f.readyzStatus, f.readyzBody
	livezStatus, livezBody := f.livezStatus, f.livezBody
	versionBody := f.versionBody
	nodesStatus, nodesBody := f.nodesStatus, f.nodesBody
	f.mu.Unlock()

	switch r.URL.Path {
	case readyzPath:
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(readyzStatus)
		_, _ = io.WriteString(w, readyzBody)
	case livezPath:
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(livezStatus)
		_, _ = io.WriteString(w, livezBody)
	case versionPath:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, versionBody)
	case nodesPath:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(nodesStatus)
		_, _ = io.WriteString(w, nodesBody)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeAPI) requestedPaths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.paths...)
}

func (f *fakeAPI) authorizations() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.auth...)
}

func observe(t *testing.T, api http.Handler, expected report.Expected, timeout time.Duration) report.Observation {
	t.Helper()
	srv, ca := tlsCluster(t, api)
	path := writeKubeconfig(t, clusterSpec{server: srv.URL, ca: ca, token: "synthetic-token"})
	if timeout == 0 {
		timeout = 2 * time.Second
	}
	client, err := New(Options{Kubeconfig: path, Expected: expected, Timeout: timeout})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client.Observe(context.Background())
}

func TestObserveGreen(t *testing.T) {
	api := newFakeAPI()
	obs := observe(t, api, report.Light, 0)

	if obs.Apiserver.Result != report.Up {
		t.Fatalf("result = %s, want up", obs.Apiserver.Result)
	}
	if obs.Apiserver.HTTPStatus == nil || *obs.Apiserver.HTTPStatus != http.StatusOK {
		t.Fatalf("http_status = %v", obs.Apiserver.HTTPStatus)
	}
	if obs.Apiserver.LatencyMS == nil {
		t.Fatal("latency_ms was not recorded")
	}
	if obs.Readyz == nil || !obs.Readyz.OK || obs.Readyz.Mode != report.ModeAggregate {
		t.Fatalf("readyz = %+v", obs.Readyz)
	}
	if obs.Server == nil || obs.Server.Flavor != "h3s" || obs.Server.Version != "v1.34.0+h3s" {
		t.Fatalf("server = %+v", obs.Server)
	}
	if obs.Nodes == nil || obs.Nodes.Total != 2 || obs.Nodes.Ready != 1 {
		t.Fatalf("nodes = %+v", obs.Nodes)
	}
	if got := obs.Nodes.Items[0].Name; got != "node-a" {
		t.Fatalf("items are not sorted by name: first = %q", got)
	}
	if obs.Nodes.Items[1].Reason != "RuntimeNotReady" || obs.Nodes.Items[1].Ready {
		t.Fatalf("node-b = %+v", obs.Nodes.Items[1])
	}
	if obs.Failure != nil {
		t.Fatalf("unexpected failure: %v", obs.Failure)
	}
}

func TestObserveVerboseReadyz(t *testing.T) {
	api := newFakeAPI()
	api.readyzStatus = http.StatusInternalServerError
	api.readyzBody = verboseFailed

	obs := observe(t, api, report.Light, 0)

	if obs.Readyz == nil || obs.Readyz.OK {
		t.Fatalf("readyz = %+v", obs.Readyz)
	}
	if obs.Readyz.Mode != report.ModeVerbose || len(obs.Readyz.Checks) != 3 {
		t.Fatalf("readyz = %+v", obs.Readyz)
	}
	last := obs.Readyz.Checks[2]
	if last.Name != "etcd" || last.OK || last.Message != "reason withheld" {
		t.Fatalf("failing check = %+v", last)
	}
	if obs.Apiserver.Livez != "ok" {
		t.Fatalf("livez = %q", obs.Apiserver.Livez)
	}
}

func TestObserveLivezOnlyWhenReadyzFails(t *testing.T) {
	green := newFakeAPI()
	observe(t, green, report.Light, 0)
	for _, path := range green.requestedPaths() {
		if path == livezPath {
			t.Fatalf("livez was requested for a healthy cluster: %v", green.requestedPaths())
		}
	}

	broken := newFakeAPI()
	broken.readyzStatus = http.StatusInternalServerError
	broken.readyzBody = "unhealthy\n"
	observe(t, broken, report.Light, 0)
	found := false
	for _, path := range broken.requestedPaths() {
		found = found || path == livezPath
	}
	if !found {
		t.Fatalf("livez was not requested: %v", broken.requestedPaths())
	}
}

func TestObserveTokenIsSent(t *testing.T) {
	api := newFakeAPI()
	observe(t, api, report.Light, 0)
	for _, header := range api.authorizations() {
		if header != "Bearer synthetic-token" {
			t.Fatalf("authorization = %q", header)
		}
	}
	if len(api.authorizations()) == 0 {
		t.Fatal("no request was recorded")
	}
}

func TestObserveReadyzUnauthorizedStops(t *testing.T) {
	api := newFakeAPI()
	api.readyzStatus = http.StatusUnauthorized
	api.readyzBody = "unauthorized\n"

	obs := observe(t, api, report.Light, 0)

	if obs.Failure == nil {
		t.Fatal("expected an auth failure")
	}
	if code := exitcode.CodeOf(obs.Failure); code != exitcode.Auth {
		t.Fatalf("exit code = %d, want %d", code, exitcode.Auth)
	}
	if obs.Failure.Reason != exitcode.AuthRejected {
		t.Fatalf("reason = %s", obs.Failure.Reason)
	}
	paths := api.requestedPaths()
	if len(paths) != 1 || paths[0] != readyzPath {
		t.Fatalf("requests after an auth refusal: %v", paths)
	}
}

func TestObserveNodeListForbiddenKeepsEvidence(t *testing.T) {
	api := newFakeAPI()
	api.nodesStatus = http.StatusForbidden
	api.nodesBody = syntheticForbidden

	obs := observe(t, api, report.Light, 0)

	if obs.Failure == nil || obs.Failure.Reason != exitcode.RBACDenied {
		t.Fatalf("failure = %v", obs.Failure)
	}
	if code := exitcode.CodeOf(obs.Failure); code != exitcode.Auth {
		t.Fatalf("exit code = %d, want %d", code, exitcode.Auth)
	}
	if !strings.Contains(obs.Failure.Message, "get,list") {
		t.Fatalf("message does not name the required verbs: %q", obs.Failure.Message)
	}
	if obs.Readyz == nil || !obs.Readyz.OK {
		t.Fatalf("partial evidence was dropped: readyz = %+v", obs.Readyz)
	}
	if obs.Nodes != nil {
		t.Fatalf("nodes should be absent, got %+v", obs.Nodes)
	}
}

func TestObserveServerAbsentWhenVersionFails(t *testing.T) {
	api := newFakeAPI()
	api.versionBody = `{"major":"1","minor":"34","gitVersion":""}`

	obs := observe(t, api, report.Light, 0)

	if obs.Server != nil {
		t.Fatalf("server = %+v, want nil", obs.Server)
	}
}

func TestObserveConnectRefusedIsDown(t *testing.T) {
	var lc net.ListenConfig
	listener, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	path := writeKubeconfig(t, clusterSpec{server: "https://" + addr})
	client, err := New(Options{Kubeconfig: path, Timeout: time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	obs := client.Observe(context.Background())

	if obs.Apiserver.Result != report.Down {
		t.Fatalf("result = %s, want down", obs.Apiserver.Result)
	}
	if obs.Failure != nil {
		t.Fatalf("a refused connect is data, not a failure: %v", obs.Failure)
	}
}

func TestObserveSilentPeerIsUnknown(t *testing.T) {
	addr := silentAddress(t)
	path := writeKubeconfig(t, clusterSpec{server: "https://" + addr})
	client, err := New(Options{Kubeconfig: path, Timeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	obs := client.Observe(context.Background())

	if obs.Apiserver.Result != report.Unknown {
		t.Fatalf("result = %s, want unknown", obs.Apiserver.Result)
	}
	if obs.Failure == nil || obs.Failure.Reason != exitcode.HTTPUnresponsive {
		t.Fatalf("failure = %v", obs.Failure)
	}
	if code := exitcode.CodeOf(obs.Failure); code != exitcode.Transport {
		t.Fatalf("exit code = %d, want %d", code, exitcode.Transport)
	}
}

func TestObserveTLSVerifyFailureIsUp(t *testing.T) {
	ok, _ := tlsCluster(t, newFakeAPI())
	foreignCA, _ := selfSignedPair(t)

	path := writeKubeconfig(t, clusterSpec{server: ok.URL, ca: foreignCA})
	client, err := New(Options{Kubeconfig: path, Timeout: time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	obs := client.Observe(context.Background())

	if obs.Apiserver.Result != report.Up {
		t.Fatalf("result = %s, want up: the peer answered", obs.Apiserver.Result)
	}
	if obs.Failure == nil || obs.Failure.Reason != exitcode.TLSVerifyFailed {
		t.Fatalf("failure = %v", obs.Failure)
	}
	if code := exitcode.CodeOf(obs.Failure); code != exitcode.TLS {
		t.Fatalf("exit code = %d, want %d", code, exitcode.TLS)
	}
}

// silentAddress listens and accepts connections without ever answering.
func silentAddress(t *testing.T) string {
	t.Helper()
	var lc net.ListenConfig
	listener, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range conns {
			_ = conn.Close()
		}
		_ = listener.Close()
	})
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
		}
	}()
	return listener.Addr().String()
}
