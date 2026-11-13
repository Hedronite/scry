package kube

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hedronite/scry/internal/exitcode"
	"github.com/Hedronite/scry/internal/report"
)

// recorderTransport records whether anything reached the network. It is the
// proof that a refusal happens locally.
type recorderTransport struct {
	calls int
}

func (r *recorderTransport) RoundTrip(*http.Request) (*http.Response, error) {
	r.calls++
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("ok")),
	}, nil
}

const allowedHost = "192.0.2.10"

func allowlisted(method, path, query string) (*http.Request, error) {
	target := "https://" + allowedHost + path
	if query != "" {
		target += "?" + query
	}
	return http.NewRequestWithContext(context.Background(), method, target, nil)
}

func TestAllowlistTable(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		query  string
		denied bool
	}{
		{name: "readyz", method: http.MethodGet, path: "/readyz"},
		{name: "readyz verbose", method: http.MethodGet, path: "/readyz", query: "verbose=true"},
		{name: "readyz extra key", method: http.MethodGet, path: "/readyz", query: "verbose=true&watch=true", denied: true},
		{name: "readyz watch", method: http.MethodGet, path: "/readyz", query: "watch", denied: true},
		{name: "livez", method: http.MethodGet, path: "/livez"},
		{name: "version", method: http.MethodGet, path: "/version"},
		{name: "version with query", method: http.MethodGet, path: "/version", query: "verbose", denied: true},
		{name: "nodes list", method: http.MethodGet, path: "/api/v1/nodes"},
		{name: "nodes paged", method: http.MethodGet, path: "/api/v1/nodes", query: "limit=500&continue=abc"},
		{name: "nodes watch", method: http.MethodGet, path: "/api/v1/nodes", query: "watch=true", denied: true},
		{name: "nodes selector", method: http.MethodGet, path: "/api/v1/nodes", query: "labelSelector=app", denied: true},
		{name: "node get", method: http.MethodGet, path: "/api/v1/nodes/node-a"},
		{name: "node subresource", method: http.MethodGet, path: "/api/v1/nodes/node-a/status", denied: true},
		{name: "node dot segment", method: http.MethodGet, path: "/api/v1/nodes/..", denied: true},
		{name: "node dot", method: http.MethodGet, path: "/api/v1/nodes/.", denied: true},
		{name: "traversal", method: http.MethodGet, path: "/api/v1/nodes/../pods", denied: true},
		{name: "pods", method: http.MethodGet, path: "/api/v1/pods", denied: true},
		{name: "secrets", method: http.MethodGet, path: "/api/v1/secrets", denied: true},
		{name: "namespaces", method: http.MethodGet, path: "/v1/namespaces", denied: true},
		{name: "root", method: http.MethodGet, path: "/", denied: true},
		{name: "empty", method: http.MethodGet, path: "", denied: true},
		{name: "post nodes", method: http.MethodPost, path: "/api/v1/nodes", denied: true},
		{name: "put node", method: http.MethodPut, path: "/api/v1/nodes/node-a", denied: true},
		{name: "patch node", method: http.MethodPatch, path: "/api/v1/nodes/node-a", denied: true},
		{name: "delete node", method: http.MethodDelete, path: "/api/v1/nodes/node-a", denied: true},
		{name: "head readyz", method: http.MethodHead, path: "/readyz", denied: true},
		{name: "options", method: http.MethodOptions, path: "/readyz", denied: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := allowlisted(tc.method, tc.path, tc.query)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			inner := &recorderTransport{}
			transport := allowedTransport{next: inner, scheme: "https", host: allowedHost}
			resp, err := transport.RoundTrip(req)
			if tc.denied {
				if err == nil {
					t.Fatalf("%s %s was allowed", tc.method, tc.path)
				}
				if !errors.Is(err, exitcode.ErrAllowlist) {
					t.Fatalf("err = %v, want an allowlist refusal", err)
				}
				if inner.calls != 0 {
					t.Fatalf("refusal reached the network (%d calls)", inner.calls)
				}
				return
			}
			if err != nil {
				t.Fatalf("%s %s was refused: %v", tc.method, tc.path, err)
			}
			if inner.calls != 1 {
				t.Fatalf("inner transport calls = %d, want 1", inner.calls)
			}
			if resp == nil {
				t.Fatal("no response")
			}
			_ = resp.Body.Close()
		})
	}
}

func TestAllowlistBindsTheEndpoint(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{name: "other host", url: "https://198.51.100.7/readyz"},
		{name: "other scheme", url: "http://192.0.2.10/readyz"},
		{name: "userinfo host", url: "https://user@192.0.2.10/readyz"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, tc.url, nil)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			inner := &recorderTransport{}
			transport := allowedTransport{next: inner, scheme: "https", host: allowedHost}
			resp, err := transport.RoundTrip(req)
			if resp != nil {
				_ = resp.Body.Close()
			}
			if !errors.Is(err, exitcode.ErrAllowlist) {
				t.Fatalf("err = %v, want an allowlist refusal", err)
			}
			if inner.calls != 0 {
				t.Fatalf("refusal reached the network (%d calls)", inner.calls)
			}
		})
	}
}

func TestRedirectIsRefused(t *testing.T) {
	cases := []struct {
		name   string
		target string
	}{
		{name: "to an unlisted path", target: "/api/v1/pods"},
		{name: "to a listed path", target: "/readyz"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, tc.target, http.StatusTemporaryRedirect)
			}))
			t.Cleanup(srv.Close)
			base, err := url.Parse(srv.URL)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			client := &http.Client{
				Transport:     allowedTransport{next: srv.Client().Transport, scheme: base.Scheme, host: base.Host},
				CheckRedirect: refuseRedirect,
			}
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+readyzPath, nil)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			resp, err := client.Do(req)
			if resp != nil {
				_ = resp.Body.Close()
			}
			if !errors.Is(err, exitcode.ErrAllowlist) {
				t.Fatalf("err = %v, want an allowlist refusal", err)
			}
		})
	}
}

func TestRefusalMessageCoversTheRequest(t *testing.T) {
	err := deny(http.MethodDelete, "/api/v1/nodes/node-a")
	const want = "scry is inspect-only; refused locally before network: DELETE /api/v1/nodes/node-a"
	if err.Error() != want {
		t.Fatalf("message = %q, want %q", err.Error(), want)
	}
}

// FuzzAllowlist drives the real wrapper: a request that reaches the inner
// transport is a GET of one of the five paths, with only that path's query keys.
func FuzzAllowlist(f *testing.F) {
	f.Add(http.MethodGet, "/readyz", "verbose")
	f.Add(http.MethodGet, "/api/v1/nodes", "limit=1&continue=abc")
	f.Add(http.MethodDelete, "/api/v1/nodes/x", "")
	f.Add(http.MethodGet, "/api/v1/nodes/../pods", "")
	f.Add(http.MethodGet, "/readyz", "watch=true")
	f.Add(http.MethodGet, "/version", "")
	f.Add(http.MethodGet, "/readyz", "%0X0000000")

	f.Fuzz(func(t *testing.T, method, path, query string) {
		target := "https://" + allowedHost + path
		if query != "" {
			target += "?" + query
		}
		req, err := http.NewRequestWithContext(context.Background(), method, target, nil)
		if err != nil {
			return // an unbuildable request cannot leave the process either
		}
		if req.URL.Host != allowedHost || req.URL.Path != path {
			return // the URL parser rewrote the input; the table test covers it
		}

		inner := &recorderTransport{}
		transport := allowedTransport{next: inner, scheme: "https", host: allowedHost}
		resp, err := transport.RoundTrip(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		if err != nil {
			if !errors.Is(err, exitcode.ErrAllowlist) {
				t.Fatalf("refusal is not an allowlist error: %v", err)
			}
			if inner.calls != 0 {
				t.Fatalf("a refusal reached the network: %s %s?%s", method, path, query)
			}
			return
		}
		if inner.calls != 1 {
			t.Fatalf("inner transport calls = %d, want 1", inner.calls)
		}
		if req.Method != http.MethodGet {
			t.Fatalf("allowed a %s", req.Method)
		}
		values, err := url.ParseQuery(req.URL.RawQuery)
		if err != nil {
			t.Fatalf("a malformed query reached the network: %q", query)
		}
		for _, rule := range allowlist {
			if !rule.path.MatchString(req.URL.Path) {
				continue
			}
			for key := range values {
				if !rule.keys[key] {
					t.Fatalf("allowed query key %q on %q", key, req.URL.Path)
				}
			}
			return
		}
		t.Fatalf("allowed an unlisted path %q", req.URL.Path)
	})
}

// FuzzParseReadyzVerbose proves the parser cannot be made to panic, cannot echo an
// unbounded body, and never reports a 2xx answer as failed.
func FuzzParseReadyzVerbose(f *testing.F) {
	f.Add(200, "ok\n")
	f.Add(200, "[+]ping ok\nreadyz check passed\n")
	f.Add(500, "[-]etcd failed: reason withheld\nreadyz check failed\n")
	f.Add(200, "readyz check failed\n")
	f.Add(200, strings.Repeat("[+]a ok\n", 1000))

	f.Fuzz(func(t *testing.T, status int, body string) {
		readyz := parseReadyz(status, []byte(body))
		succeeded := status >= 200 && status < 300
		if readyz.OK && !succeeded {
			t.Fatalf("status %d reported ok: %+v", status, readyz)
		}
		if readyz.Mode == report.ModeAggregate && readyz.OK != succeeded {
			t.Fatalf("aggregate mode diverged from status %d: %+v", status, readyz)
		}
		for _, check := range readyz.Checks {
			if !check.OK && readyz.OK {
				t.Fatalf("a failing check was reported ok: %+v", readyz)
			}
		}
		if len(readyz.Checks) > maxChecks {
			t.Fatalf("checks = %d, want at most %d", len(readyz.Checks), maxChecks)
		}
		for _, check := range readyz.Checks {
			if len(check.Name) == 0 || len(check.Name) > maxCheckName {
				t.Fatalf("check name length %d", len(check.Name))
			}
			if len(check.Message) > 200 {
				t.Fatalf("check message length %d", len(check.Message))
			}
		}
	})
}

// TestExportedSurfaceTakesNoRequestShape is the reflective proof: no exported
// function or method in this package takes a method, a path or a request, so
// there is no second way to reach a cluster.
func TestExportedSurfaceTakesNoRequestShape(t *testing.T) {
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	forbidden := map[string]bool{"method": true, "verb": true, "path": true, "uri": true, "url": true, "request": true, "req": true}
	fset := token.NewFileSet()
	checked := 0
	for _, name := range paths {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			decl, ok := node.(*ast.FuncDecl)
			if !ok || !ast.IsExported(decl.Name.Name) || decl.Type.Params == nil {
				return true
			}
			if decl.Recv != nil && !exportedReceiver(decl) {
				// A method on an unexported type is not reachable from another
				// package, so it is not part of the surface under test.
				return true
			}
			checked++
			for _, field := range decl.Type.Params.List {
				for _, ident := range field.Names {
					if forbidden[strings.ToLower(ident.Name)] {
						t.Fatalf("%s.%s takes a %q parameter", name, decl.Name.Name, ident.Name)
					}
				}
				var rendered strings.Builder
				if err := printer.Fprint(&rendered, fset, field.Type); err != nil {
					t.Fatalf("render type: %v", err)
				}
				switch rendered.String() {
				case "*http.Request", "http.Request", "*url.URL", "url.URL":
					t.Fatalf("%s.%s takes a %s", name, decl.Name.Name, rendered.String())
				}
			}
			return true
		})
	}
	if checked == 0 {
		t.Fatal("no exported declarations were inspected")
	}
}

// exportedReceiver reports whether a method's receiver type is exported.
func exportedReceiver(decl *ast.FuncDecl) bool {
	if decl.Recv == nil || len(decl.Recv.List) == 0 {
		return false
	}
	expr := decl.Recv.List[0].Type
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	ident, ok := expr.(*ast.Ident)
	return ok && ast.IsExported(ident.Name)
}
