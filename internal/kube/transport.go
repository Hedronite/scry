package kube

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/Hedronite/scry/internal/exitcode"
)

// The allowlist is the D4 table: an all-GET matrix of path and per-path query
// keys. Anything not matched here is refused before dial.
const (
	readyzPath  = "/readyz"
	livezPath   = "/livez"
	versionPath = "/version"
	nodesPath   = "/api/v1/nodes"
)

type pathRule struct {
	path *regexp.Regexp
	keys map[string]bool // allowed query keys; nil means no query at all
}

var allowlist = []pathRule{
	{path: regexp.MustCompile(`^/readyz$`), keys: map[string]bool{"verbose": true}},
	{path: regexp.MustCompile(`^/livez$`), keys: map[string]bool{"verbose": true}},
	{path: regexp.MustCompile(`^/version$`)},
	{path: regexp.MustCompile(`^/api/v1/nodes$`), keys: map[string]bool{"limit": true, "continue": true}},
	{path: regexp.MustCompile(`^/api/v1/nodes/[^/]+$`)},
}

// maxDeniedPath bounds the path echoed in a refusal so a hostile value cannot
// flood a terminal or a log.
const maxDeniedPath = 200

// allowed reports whether one request may leave the process. The path must match
// a rule exactly, carry no dot segment (a server may normalise ".." onto a
// different resource) and use only that rule's query keys.
func allowed(method, path string, query url.Values) error {
	if method != http.MethodGet {
		return deny(method, path)
	}
	if hasDotSegment(path) {
		return deny(method, path)
	}
	for _, rule := range allowlist {
		if !rule.path.MatchString(path) {
			continue
		}
		for key := range query {
			if !rule.keys[key] {
				return deny(method, path)
			}
		}
		return nil
	}
	return deny(method, path)
}

// hasDotSegment reports a "." or ".." segment, or an empty one.
func hasDotSegment(path string) bool {
	for _, segment := range strings.Split(path, "/") {
		if segment == "." || segment == ".." {
			return true
		}
	}
	return false
}

// deny returns the frozen refusal message. It carries no server URL, no
// credential and no query value: the query may hold an opaque continue token.
func deny(method, path string) error {
	return exitcode.New(exitcode.AllowlistDenied,
		"scry is inspect-only; refused locally before network: %s %s",
		printableToken(method), printableToken(path))
}

// allowedTransport refuses anything outside the allowlist before delegating. It
// is the outermost wrapper on every client, so redirect hops re-enter it.
//
// It also binds the request to the one endpoint the kubeconfig named: the host
// check is what stops an absolute-URI request, a redirect or a future code path
// from carrying the credential to another server with an allowlisted path.
type allowedTransport struct {
	next   http.RoundTripper
	scheme string
	host   string
}

func (t allowedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL == nil || r.URL.User != nil ||
		!strings.EqualFold(r.URL.Scheme, t.scheme) || !strings.EqualFold(r.URL.Host, t.host) {
		// Userinfo is refused too: Scry authenticates with the kubeconfig's
		// client certificate or token, so a URL credential means the request was
		// built by something other than this package.
		return nil, deny(r.Method, pathOf(r))
	}
	// ParseQuery, not URL.Query(): Query() silently drops a malformed pair,
	// which would let an unread key through.
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, deny(r.Method, r.URL.Path)
	}
	if err := allowed(r.Method, r.URL.Path, query); err != nil {
		return nil, err
	}
	// One fresh connection per request: the allowlisted endpoints are cheap, and
	// a poller must never ride a pooled connection or a rotated certificate. The
	// request is copied because a RoundTripper may not modify its argument.
	cloned := *r
	cloned.Close = true
	return t.next.RoundTrip(&cloned)
}

// pathOf is the printable target of a request whose URL may not be usable.
func pathOf(r *http.Request) string {
	if r.URL == nil {
		return ""
	}
	return r.URL.Path
}

// refuseRedirect ends the request at the first redirect: no allowlisted endpoint
// redirects, so a redirect is either a misdirected server or an attack.
func refuseRedirect(req *http.Request, _ []*http.Request) error {
	return deny(req.Method, pathOf(req))
}

// dialTrace records whether the TCP connect for one request completed, so a
// failure can be classified from evidence rather than from error text.
type dialTrace struct {
	started   atomic.Bool
	completed atomic.Bool
}

type dialTraceKey struct{}

func withDialTrace(ctx context.Context, t *dialTrace) context.Context {
	return context.WithValue(ctx, dialTraceKey{}, t)
}

func dialTraceFrom(ctx context.Context) *dialTrace {
	t, _ := ctx.Value(dialTraceKey{}).(*dialTrace)
	return t
}

// traceDial wraps a dial function so each request's connect is recorded.
func traceDial(next func(ctx context.Context, network, addr string) (net.Conn, error)) func(ctx context.Context, network, addr string) (net.Conn, error) {
	if next == nil {
		var dialer net.Dialer
		next = dialer.DialContext
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if t := dialTraceFrom(ctx); t != nil {
			t.started.Store(true)
		}
		conn, err := next(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		if t := dialTraceFrom(ctx); t != nil {
			t.completed.Store(true)
		}
		return conn, nil
	}
}

// printableToken strips control characters and bounds a value that came from
// outside this process (a method or a path).
func printableToken(s string) string {
	cleaned := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if len(cleaned) > maxDeniedPath {
		cleaned = cleaned[:maxDeniedPath]
	}
	return cleaned
}
