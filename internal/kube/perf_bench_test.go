package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Hedronite/scry/internal/fakecluster"
	"github.com/Hedronite/scry/internal/report"
)

// benchSink keeps a benchmarked value alive so the compiler cannot drop the work.
var benchSink report.Readyz

// benchVerboseBody builds a stock-kube-apiserver style verbose body: every check
// passes except the last, so the parser walks the fail path too.
func benchVerboseBody(checks int) []byte {
	var b strings.Builder
	for i := range checks - 1 {
		fmt.Fprintf(&b, "[+]check-%02d ok\n", i)
	}
	b.WriteString("[-]etcd failed: reason withheld\nreadyz check failed\n")
	return []byte(b.String())
}

func BenchmarkParseReadyzVerbose(b *testing.B) {
	bodies := []struct {
		name string
		body []byte
	}{
		{name: "bare-ok", body: []byte("ok\n")},
		{name: "8-checks", body: benchVerboseBody(8)},
		{name: "64-checks", body: benchVerboseBody(64)},
	}
	for _, tc := range bodies {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(tc.body)))
			for b.Loop() {
				benchSink = parseReadyz(http.StatusOK, tc.body)
			}
		})
	}
}

// benchNodes builds a synthetic node list of the given size. Every fourth node is
// not ready and every sixteenth stopped reporting, so the report takes every
// branch.
func benchNodes(count int) []fakecluster.Node {
	nodes := make([]fakecluster.Node, 0, count)
	for i := range count {
		node := fakecluster.Node{Name: fmt.Sprintf("node-%04d", i), Ready: "True", Reason: "KubeletReady"}
		switch {
		case i%16 == 0:
			node.Ready, node.Reason = "Unknown", "NodeStatusUnknown"
		case i%4 == 0:
			node.Ready, node.Reason = "False", "RuntimeNotReady"
		}
		if i%32 == 0 {
			node.NetworkUnavailable = "True"
		}
		nodes = append(nodes, node)
	}
	return nodes
}

// benchClient builds a client pointed at a fixture, and returns it with a
// request context.
func benchClient(b *testing.B, opts fakecluster.Options) (*Client, context.Context) {
	b.Helper()
	server, err := fakecluster.New(opts)
	if err != nil {
		b.Fatalf("fake cluster: %v", err)
	}
	b.Cleanup(server.Close)
	path := filepath.Join(b.TempDir(), "kubeconfig")
	if _, err := server.Kubeconfig(path, nil); err != nil {
		b.Fatalf("kubeconfig: %v", err)
	}
	client, err := New(Options{Kubeconfig: path})
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	return client, context.Background()
}

// BenchmarkNodesReport5000 prices the shipped path: one allowlisted LIST whose
// answer is 5,000 nodes, decoded and turned into the report.
func BenchmarkNodesReport5000(b *testing.B) {
	body := fakecluster.NodeList(benchNodes(5000)...)
	client, ctx := benchClient(b, fakecluster.Options{
		Nodes: fakecluster.Reply{Body: body},
	})

	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	for b.Loop() {
		nodes, failure := client.nodes(ctx)
		if failure != nil {
			b.Fatalf("nodes: %v", failure)
		}
		if nodes.Total != 5000 {
			b.Fatalf("total = %d", nodes.Total)
		}
	}
}

// BenchmarkNodesReportPaged5000 prices the wire half of pagination: the same
// 5,000 nodes served in ten pages of 500 through the allowlisted transport. The
// client lists once today, so this documents what a paging client would pay
// rather than what status pays now.
func BenchmarkNodesReportPaged5000(b *testing.B) {
	const (
		pages    = 10
		pageSize = 500
	)
	all := benchNodes(pages * pageSize)
	pageBodies := make([]string, pages)
	totalBytes := 0
	for page := range pages {
		pageBodies[page] = fakecluster.NodeList(all[page*pageSize : (page+1)*pageSize]...)
		totalBytes += len(pageBodies[page])
	}

	client, ctx := benchClient(b, fakecluster.Options{
		Handle: func(w http.ResponseWriter, r *http.Request) bool {
			if r.URL.Path != nodesPath {
				return false
			}
			page := 0
			if token := r.URL.Query().Get("continue"); token != "" {
				parsed, err := strconv.Atoi(token)
				if err != nil || parsed < 0 || parsed >= pages {
					http.Error(w, "bad continue token", http.StatusBadRequest)
					return true
				}
				page = parsed
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, pageBodies[page])
			return true
		},
	})

	b.ReportAllocs()
	b.SetBytes(int64(totalBytes))
	for b.Loop() {
		for page := range pages {
			query := url.Values{"limit": {strconv.Itoa(pageSize)}}
			if page > 0 {
				query.Set("continue", strconv.Itoa(page))
			}
			answer := client.get(ctx, nodesPath, query)
			if answer.failure != nil || answer.status != http.StatusOK {
				b.Fatalf("page %d: status %d failure %v", page, answer.status, answer.failure)
			}
		}
	}
}

// BenchmarkNodesWire5000 is the same 5,000-node answer fetched through the
// allowlisted transport with no decoding, so the report's own cost reads as the
// difference from BenchmarkNodesReport5000.
func BenchmarkNodesWire5000(b *testing.B) {
	body := fakecluster.NodeList(benchNodes(5000)...)
	client, ctx := benchClient(b, fakecluster.Options{
		Nodes: fakecluster.Reply{Body: body},
	})

	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	for b.Loop() {
		answer := client.get(ctx, nodesPath, nil)
		if answer.failure != nil || answer.status != http.StatusOK {
			b.Fatalf("status %d failure %v", answer.status, answer.failure)
		}
		benchBytes = answer.body
	}
}

// BenchmarkNodesJSONDecode5000 prices decoding the same answer with
// encoding/json into the few fields the report uses, which is the alternative to
// the typed client.
func BenchmarkNodesJSONDecode5000(b *testing.B) {
	body := []byte(fakecluster.NodeList(benchNodes(5000)...))
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	for b.Loop() {
		var list jsonNodeList
		if err := json.Unmarshal(body, &list); err != nil {
			b.Fatalf("decode: %v", err)
		}
		if len(list.Items) != 5000 {
			b.Fatalf("items = %d", len(list.Items))
		}
		benchNodeSink = list
	}
}

// jsonNodeList is the minimal shape the report needs, used only by the benchmark
// above to price the decode.
type jsonNodeList struct {
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Status struct {
			Conditions []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
				Reason string `json:"reason"`
			} `json:"conditions"`
		} `json:"status"`
	} `json:"items"`
}

var (
	benchBytes    []byte
	benchNodeSink jsonNodeList
)

// benchResponse is a trivial answer so the allowlist benchmark measures the
// wrapper rather than a response allocation.
var benchResponse = &http.Response{
	StatusCode: http.StatusOK,
	Header:     http.Header{},
	Body:       http.NoBody,
}

type benchTransport struct{}

func (benchTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return benchResponse, nil
}

func BenchmarkAllowlistRoundTrip(b *testing.B) {
	cases := []struct {
		name    string
		method  string
		path    string
		query   string
		host    string
		refused bool
	}{
		{name: "allowed-readyz-verbose", method: http.MethodGet, path: readyzPath, query: "verbose=true", host: "apiserver.invalid"},
		{name: "allowed-nodes-paged", method: http.MethodGet, path: nodesPath, query: "limit=500&continue=abc", host: "apiserver.invalid"},
		{name: "refused-delete", method: http.MethodDelete, path: nodesPath + "/node-a", host: "apiserver.invalid", refused: true},
		{name: "refused-watch", method: http.MethodGet, path: nodesPath, query: "watch=true", host: "apiserver.invalid", refused: true},
		{name: "refused-other-host", method: http.MethodGet, path: readyzPath, host: "elsewhere.invalid", refused: true},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			target := "https://" + tc.host + tc.path
			if tc.query != "" {
				target += "?" + tc.query
			}
			req, err := http.NewRequestWithContext(context.Background(), tc.method, target, nil)
			if err != nil {
				b.Fatalf("request: %v", err)
			}
			transport := allowedTransport{next: benchTransport{}, scheme: "https", host: "apiserver.invalid"}
			b.ReportAllocs()
			for b.Loop() {
				resp, err := transport.RoundTrip(req)
				if tc.refused && err == nil {
					b.Fatal("refused case was allowed")
				}
				if !tc.refused && err != nil {
					b.Fatalf("allowed case was refused: %v", err)
				}
				if resp != nil {
					_ = resp.Body.Close()
				}
			}
		})
	}
}
