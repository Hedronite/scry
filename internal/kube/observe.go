package kube

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hedronite/scry/internal/exitcode"
	"github.com/Hedronite/scry/internal/report"
)

const (
	maxBodyBytes = 64 << 10
	maxNodeName  = 253

	flavorH3SSuffix = "+h3s"
)

// attempt is one HTTP request's evidence.
type attempt struct {
	status    int
	body      []byte
	latencyMS int64
	result    report.Result
	failure   *exitcode.Error
}

// Observe gathers the three signals and returns raw evidence. It stops early
// when a later request could not add anything.
func (c *Client) Observe(ctx context.Context) report.Observation {
	obs := report.Observation{Cluster: c.cluster}

	answer := c.get(ctx, readyzPath, url.Values{"verbose": {"true"}})
	obs.Apiserver = report.Apiserver{Result: answer.result}
	if answer.status != 0 {
		status, latency := answer.status, answer.latencyMS
		obs.Apiserver.HTTPStatus = &status
		obs.Apiserver.LatencyMS = &latency
	}
	if answer.failure != nil {
		obs.Failure = answer.failure
		return obs
	}
	if answer.result == report.Down {
		// Nothing else can answer either, and a dark apiserver is a state, not a
		// failure: expected-dark decides the severity in Build.
		return obs
	}
	if reason, ok := authReason(answer.status); ok {
		obs.Failure = exitcode.New(reason, "%s", authMessage(reason))
		return obs
	}

	readyz := parseReadyz(answer.status, answer.body)
	obs.Readyz = &readyz
	if !readyz.OK {
		// Diagnostic only: on h3s this is the only thing separating "serving"
		// from "serving and claiming readiness".
		obs.Apiserver.Livez = livezVerdict(c.get(ctx, livezPath, url.Values{"verbose": {"true"}}))
	}
	if server := c.server(ctx); server != nil {
		obs.Server = server
	}
	nodes, failure := c.nodes(ctx)
	if failure != nil {
		obs.Failure = failure
		return obs
	}
	obs.Nodes = nodes
	return obs
}

// get performs one request and retries once when the peer went silent. A refused
// connect is never retried: it will refuse again.
func (c *Client) get(ctx context.Context, path string, query url.Values) attempt {
	first := c.getOnce(ctx, path, query)
	if first.failure == nil || first.result != report.Unknown {
		return first
	}
	return c.getOnce(ctx, path, query)
}

func (c *Client) getOnce(ctx context.Context, path string, query url.Values) attempt {
	trace := &dialTrace{}
	requestCtx, cancel := context.WithTimeout(withDialTrace(ctx, trace), c.timeout)
	defer cancel()

	target := *c.base
	target.Path = strings.TrimSuffix(c.base.Path, "/") + path
	target.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, target.String(), nil)
	if err != nil {
		return attempt{
			result:  report.Unknown,
			failure: exitcode.Wrap(exitcode.InternalError, err, "could not build the %s request", path),
		}
	}
	req.Header.Set("Accept", "application/json, text/plain")

	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		out := classifyTransportError(err, trace)
		return attempt{result: out.result, failure: out.failure}
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	latency := time.Since(start).Milliseconds()
	if err != nil {
		out := classifyTransportError(err, trace)
		return attempt{result: out.result, failure: out.failure}
	}
	return attempt{result: report.Up, status: resp.StatusCode, body: body, latencyMS: latency}
}

// server reads GET /version. It is diagnostic: a failure leaves Server nil and
// never changes the verdict.
func (c *Client) server(ctx context.Context) *report.Server {
	answer := c.get(ctx, versionPath, nil)
	if answer.failure != nil || answer.status != http.StatusOK {
		return nil
	}
	var info struct {
		GitVersion string `json:"gitVersion"`
	}
	if err := json.Unmarshal(answer.body, &info); err != nil {
		return nil
	}
	version := clampString(info.GitVersion, 64)
	if version == "" {
		return nil
	}
	flavor := "kubernetes"
	if strings.HasSuffix(version, flavorH3SSuffix) {
		flavor = "h3s"
	}
	return &report.Server{Version: version, Flavor: flavor}
}

// nodes lists nodes. Node conditions are the only real readiness signal h3s has.
func (c *Client) nodes(ctx context.Context) (*report.Nodes, *exitcode.Error) {
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	list, err := c.nodesClient.Nodes().List(requestCtx, metav1.ListOptions{})
	if err != nil {
		return nil, nodeListFailure(err)
	}
	nodes := &report.Nodes{Total: len(list.Items), Items: make([]report.Node, 0, len(list.Items))}
	for i := range list.Items {
		item := &list.Items[i]
		node := report.Node{Name: clampString(item.Name, maxNodeName)}
		for _, condition := range item.Status.Conditions {
			switch condition.Type {
			case corev1.NodeReady:
				node.Ready = condition.Status == corev1.ConditionTrue
				node.Reason = clampString(condition.Reason, report.MaxReasonLen)
			case corev1.NodeNetworkUnavailable:
				node.NetworkUnavailable = node.NetworkUnavailable || condition.Status == corev1.ConditionTrue
			default:
			}
		}
		if node.Ready {
			nodes.Ready++
		}
		nodes.Items = append(nodes.Items, node)
	}
	sort.SliceStable(nodes.Items, func(i, j int) bool { return nodes.Items[i].Name < nodes.Items[j].Name })
	return nodes, nil
}

func authReason(status int) (exitcode.Reason, bool) {
	switch status {
	case http.StatusUnauthorized:
		return exitcode.AuthRejected, true
	case http.StatusForbidden:
		return exitcode.RBACDenied, true
	default:
		return "", false
	}
}

func authMessage(reason exitcode.Reason) string {
	if reason == exitcode.RBACDenied {
		return "the credential may not read /readyz; grant get on the /readyz non-resource URL"
	}
	return "the apiserver rejected the credential"
}

func nodeListFailure(err error) *exitcode.Error {
	switch {
	case apierrors.IsUnauthorized(err):
		return exitcode.Wrap(exitcode.AuthRejected, err, "the apiserver rejected the credential")
	case apierrors.IsForbidden(err):
		return exitcode.Wrap(exitcode.RBACDenied, err, "the credential may not list nodes; grant get,list on nodes")
	}
	if out := classifyTransportError(err, nil); out.failure != nil {
		return out.failure
	}
	return exitcode.Wrap(exitcode.HTTPUnresponsive, err, "the node list returned no answer")
}

func livezVerdict(live attempt) string {
	if live.failure != nil || live.status < 200 || live.status > 299 {
		return "failed"
	}
	return "ok"
}
