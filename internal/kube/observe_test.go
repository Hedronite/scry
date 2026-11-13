package kube

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/Hedronite/scry/internal/exitcode"
	"github.com/Hedronite/scry/internal/fakecluster"
	"github.com/Hedronite/scry/internal/report"
)

const (
	verboseFailed   = "[+]ping ok\n[+]log ok\n[-]etcd failed: reason withheld\nreadyz check failed\n"
	syntheticDenied = `{"kind":"Status","apiVersion":"v1","status":"Failure","message":"nodes is forbidden","reason":"Forbidden","code":403}`
)

// scriptedNodes is the two-node list most tests start from: one ready, one whose
// runtime is not up.
func scriptedNodes() string {
	return fakecluster.NodeList(
		fakecluster.Node{Name: "node-b", Ready: "False", Reason: "RuntimeNotReady"},
		fakecluster.Node{Name: "node-a", Ready: "True", Reason: "KubeletReady"},
	)
}

// observe starts a fixture, points a kubeconfig at it, and returns both.
func observe(t *testing.T, opts fakecluster.Options, expected report.Expected, timeout time.Duration) (*fakecluster.Server, report.Observation) {
	t.Helper()
	server, err := fakecluster.New(opts)
	if err != nil {
		t.Fatalf("fake cluster: %v", err)
	}
	t.Cleanup(server.Close)
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if _, err := server.Kubeconfig(path, nil); err != nil {
		t.Fatalf("kubeconfig: %v", err)
	}
	if timeout == 0 {
		timeout = 2 * time.Second
	}
	client, err := New(Options{Kubeconfig: path, Expected: expected, Timeout: timeout})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return server, client.Observe(context.Background())
}

func TestObserveGreen(t *testing.T) {
	server, obs := observe(t, fakecluster.Options{
		Nodes: fakecluster.Reply{Body: scriptedNodes()},
	}, report.Light, 0)

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
	if obs.Nodes.Items[1].Reason != "RuntimeNotReady" || obs.Nodes.Items[1].Ready || obs.Nodes.Items[1].Unknown {
		t.Fatalf("node-b = %+v", obs.Nodes.Items[1])
	}
	if obs.Failure != nil {
		t.Fatalf("unexpected failure: %v", obs.Failure)
	}
	if paths := server.Paths(); strings.Join(paths, ",") != "/readyz,/version,/api/v1/nodes" {
		t.Fatalf("paths = %v", paths)
	}
}

func TestObserveNodeUnknownComesFromTheConditionStatus(t *testing.T) {
	_, obs := observe(t, fakecluster.Options{
		Nodes: fakecluster.Reply{Body: fakecluster.NodeList(
			fakecluster.Node{Name: "node-a", Ready: "True", Reason: "KubeletReady"},
			fakecluster.Node{Name: "node-b", Ready: "Unknown", Reason: "NodeStatusUnknown"},
		)},
	}, report.Light, 0)

	if obs.Nodes == nil || obs.Nodes.Total != 2 || obs.Nodes.Ready != 1 {
		t.Fatalf("nodes = %+v, want 2 total and 1 ready", obs.Nodes)
	}
	unknown := obs.Nodes.Items[1]
	if !unknown.Unknown || unknown.Ready {
		t.Fatalf("node-b = %+v, want unknown and not ready", unknown)
	}
	if unknown.Reason != "NodeStatusUnknown" {
		t.Fatalf("reason = %q, want it kept verbatim", unknown.Reason)
	}
}

func TestObserveUsesAFreshConnectionPerRequest(t *testing.T) {
	server, obs := observe(t, fakecluster.Options{}, report.Light, 0)
	if obs.Failure != nil {
		t.Fatalf("observe: %v", obs.Failure)
	}
	requests := server.Requests()
	if len(requests) != 3 {
		t.Fatalf("requests = %d, want 3", len(requests))
	}
	seen := map[int]bool{}
	for _, request := range requests {
		if !request.Close {
			t.Fatalf("%s did not ask for the connection to close", request.Path)
		}
		if seen[request.Connection] {
			t.Fatalf("%s reused connection %d", request.Path, request.Connection)
		}
		seen[request.Connection] = true
	}
	if server.Connections() != 3 {
		t.Fatalf("connections = %d, want 3", server.Connections())
	}
}

func TestObserveSendsOnlyAllowlistedGets(t *testing.T) {
	server, _ := observe(t, fakecluster.Options{
		Readyz: fakecluster.Reply{Status: http.StatusInternalServerError, Body: verboseFailed},
	}, report.Light, 0)

	for _, request := range server.Requests() {
		if request.Method != http.MethodGet {
			t.Fatalf("%s %s", request.Method, request.Path)
		}
		if strings.Contains(request.Query, "watch") {
			t.Fatalf("%s carried a watch parameter: %q", request.Path, request.Query)
		}
	}
	allowed := map[string]bool{
		"/readyz": true, "/livez": true, "/version": true, "/api/v1/nodes": true,
	}
	for _, request := range server.Requests() {
		if !allowed[request.Path] {
			t.Fatalf("unlisted path %q", request.Path)
		}
	}
}

func TestObserveVerboseReadyz(t *testing.T) {
	_, obs := observe(t, fakecluster.Options{
		Readyz: fakecluster.Reply{Status: http.StatusInternalServerError, Body: verboseFailed},
	}, report.Light, 0)

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
	healthy, _ := observe(t, fakecluster.Options{}, report.Light, 0)
	if healthy.Count(fakecluster.LivezPath) != 0 {
		t.Fatalf("livez was requested for a healthy cluster: %v", healthy.Paths())
	}

	broken, _ := observe(t, fakecluster.Options{
		Readyz: fakecluster.Reply{Status: http.StatusInternalServerError, Body: "unhealthy\n"},
	}, report.Light, 0)
	if broken.Count(fakecluster.LivezPath) != 1 {
		t.Fatalf("livez requests = %d, want 1: %v", broken.Count(fakecluster.LivezPath), broken.Paths())
	}
}

func TestObserveTokenIsSent(t *testing.T) {
	server, _ := observe(t, fakecluster.Options{}, report.Light, 0)
	requests := server.Requests()
	if len(requests) == 0 {
		t.Fatal("no request was recorded")
	}
	want := "Bearer " + fakecluster.DefaultToken
	for _, request := range requests {
		if request.Authorization != want {
			t.Fatalf("%s authorization = %q, want %q", request.Path, request.Authorization, want)
		}
	}
}

func TestObserveReadyzUnauthorizedStops(t *testing.T) {
	server, obs := observe(t, fakecluster.Options{
		Readyz: fakecluster.Reply{Status: http.StatusUnauthorized, Body: "unauthorized\n"},
	}, report.Light, 0)

	if obs.Failure == nil {
		t.Fatal("expected an auth failure")
	}
	if code := exitcode.CodeOf(obs.Failure); code != exitcode.Auth {
		t.Fatalf("exit code = %d, want %d", code, exitcode.Auth)
	}
	if obs.Failure.Reason != exitcode.AuthRejected {
		t.Fatalf("reason = %s", obs.Failure.Reason)
	}
	if paths := server.Paths(); len(paths) != 1 || paths[0] != fakecluster.ReadyzPath {
		t.Fatalf("requests after an auth refusal: %v", paths)
	}
}

func TestObserveNodeListForbiddenKeepsEvidence(t *testing.T) {
	_, obs := observe(t, fakecluster.Options{
		Nodes: fakecluster.Reply{Status: http.StatusForbidden, Body: syntheticDenied},
	}, report.Light, 0)

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
	_, obs := observe(t, fakecluster.Options{
		Version: fakecluster.Reply{Body: `{"major":"1","minor":"34","gitVersion":""}`},
	}, report.Light, 0)

	if obs.Server != nil {
		t.Fatalf("server = %+v, want nil", obs.Server)
	}
}

func TestObserveStockFlavor(t *testing.T) {
	_, obs := observe(t, fakecluster.Options{
		Version: fakecluster.Reply{Body: fakecluster.VersionStock},
	}, report.Light, 0)

	if obs.Server == nil || obs.Server.Flavor != "kubernetes" {
		t.Fatalf("server = %+v", obs.Server)
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
	path := writeBareKubeconfig(t, "https://"+addr, nil)

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

func TestObserveSilentPeerIsUnknownAndRetriedOnce(t *testing.T) {
	server, obs := observe(t, fakecluster.Options{
		Readyz: fakecluster.Reply{Hang: true},
	}, report.Light, 150*time.Millisecond)

	if obs.Apiserver.Result != report.Unknown {
		t.Fatalf("result = %s, want unknown", obs.Apiserver.Result)
	}
	if obs.Failure == nil || obs.Failure.Reason != exitcode.HTTPUnresponsive {
		t.Fatalf("failure = %v", obs.Failure)
	}
	if code := exitcode.CodeOf(obs.Failure); code != exitcode.Transport {
		t.Fatalf("exit code = %d, want %d", code, exitcode.Transport)
	}
	if got := server.Count(fakecluster.ReadyzPath); got != 2 {
		t.Fatalf("silent peer saw %d request(s), want 2 (one retry)", got)
	}
}

func TestObserveTLSStallIsUnknown(t *testing.T) {
	addr := silentAddress(t)
	path := writeBareKubeconfig(t, "https://"+addr, nil)
	client, err := New(Options{Kubeconfig: path, Timeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	obs := client.Observe(context.Background())

	if obs.Apiserver.Result != report.Unknown {
		t.Fatalf("result = %s, want unknown: TCP completed, nothing answered", obs.Apiserver.Result)
	}
	if obs.Failure == nil || obs.Failure.Reason != exitcode.HTTPUnresponsive {
		t.Fatalf("failure = %v", obs.Failure)
	}
}

func TestObserveTLSVerifyFailureIsUp(t *testing.T) {
	server, err := fakecluster.New(fakecluster.Options{})
	if err != nil {
		t.Fatalf("fake cluster: %v", err)
	}
	t.Cleanup(server.Close)
	stranger, err := fakecluster.New(fakecluster.Options{})
	if err != nil {
		t.Fatalf("stranger: %v", err)
	}
	t.Cleanup(stranger.Close)

	path := filepath.Join(t.TempDir(), "kubeconfig")
	if _, err := server.Kubeconfig(path, func(config *clientcmdapi.Config) {
		config.Clusters["cluster"].CertificateAuthorityData = stranger.CAPEM()
	}); err != nil {
		t.Fatalf("kubeconfig: %v", err)
	}
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

func TestObserveKindNodeListDecodesEmptyItems(t *testing.T) {
	_, obs := observe(t, fakecluster.Options{
		Nodes: fakecluster.Reply{Body: `{"apiVersion":"v1","kind":"NodeList","items":[]}`},
	}, report.Light, 0)

	if obs.Nodes == nil || obs.Nodes.Total != 0 || len(obs.Nodes.Items) != 0 {
		t.Fatalf("nodes = %+v", obs.Nodes)
	}
}

// silentAddress listens and accepts connections without ever answering, so the
// TLS handshake stalls after a completed connect.
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
