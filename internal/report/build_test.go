package report

import (
	"strings"
	"testing"
	"time"

	"github.com/Hedronite/scry/internal/exitcode"
)

func observed(nodes *Nodes, readyz *Readyz) Observation {
	return Observation{
		Cluster:   Cluster{Alias: "lab-one", Expected: Light},
		Apiserver: Apiserver{Result: Up},
		Readyz:    readyz,
		Nodes:     nodes,
	}
}

func readyOK() *Readyz { return &Readyz{Mode: ModeAggregate, OK: true, Checks: []Check{}} }

func nodesOf(items ...Node) *Nodes {
	total, ready := len(items), 0
	for _, item := range items {
		if item.Ready {
			ready++
		}
	}
	return &Nodes{Total: total, Ready: ready, Items: items}
}

func TestBuildObservedSeverity(t *testing.T) {
	cases := []struct {
		name     string
		obs      Observation
		want     Severity
		wantCode exitcode.Code
		reasons  []exitcode.Reason
	}{
		{
			name:     "all green",
			obs:      observed(nodesOf(Node{Name: "node-a", Ready: true}), readyOK()),
			want:     Green,
			wantCode: exitcode.Green,
		},
		{
			name:     "one node not ready is degraded",
			obs:      observed(nodesOf(Node{Name: "node-a", Ready: true}, Node{Name: "node-b", Reason: "RuntimeNotReady"}), readyOK()),
			want:     Yellow,
			wantCode: exitcode.Yellow,
			reasons:  []exitcode.Reason{exitcode.NodeNotReady},
		},
		{
			name:     "one node unknown is degraded",
			obs:      observed(nodesOf(Node{Name: "node-a", Ready: true}, Node{Name: "node-b", Unknown: true, Reason: "NodeStatusUnknown"}), readyOK()),
			want:     Yellow,
			wantCode: exitcode.Yellow,
			reasons:  []exitcode.Reason{exitcode.NodeNotReady},
		},
		{
			name:     "unknown and not ready with one ready is degraded",
			obs:      observed(nodesOf(Node{Name: "node-a", Ready: true}, Node{Name: "node-b", Unknown: true}, Node{Name: "node-c"}), readyOK()),
			want:     Yellow,
			wantCode: exitcode.Yellow,
			reasons:  []exitcode.Reason{exitcode.NodeNotReady},
		},
		{
			name:     "no node ready is red",
			obs:      observed(nodesOf(Node{Name: "node-a", Reason: "RuntimeNotReady"}, Node{Name: "node-b", Reason: "KubeletReady"}), readyOK()),
			want:     Red,
			wantCode: exitcode.Red,
			reasons:  []exitcode.Reason{exitcode.NodeNotReady},
		},
		{
			name:     "every node unknown is red",
			obs:      observed(nodesOf(Node{Name: "node-a", Unknown: true, Reason: "NodeStatusUnknown"}, Node{Name: "node-b", Unknown: true, Reason: "NodeStatusUnknown"}), readyOK()),
			want:     Red,
			wantCode: exitcode.Red,
			reasons:  []exitcode.Reason{exitcode.NodeNotReady},
		},
		{
			name:     "unknown mixed with not ready and nothing ready is red",
			obs:      observed(nodesOf(Node{Name: "node-a", Unknown: true}, Node{Name: "node-b", Reason: "RuntimeNotReady"}), readyOK()),
			want:     Red,
			wantCode: exitcode.Red,
			reasons:  []exitcode.Reason{exitcode.NodeNotReady},
		},
		{
			name:     "no nodes registered is red",
			obs:      observed(nodesOf(), readyOK()),
			want:     Red,
			wantCode: exitcode.Red,
			reasons:  []exitcode.Reason{exitcode.NoNodes},
		},
		{
			name:     "readyz failure is red",
			obs:      observed(nodesOf(Node{Name: "node-a", Ready: true}), &Readyz{Mode: ModeVerbose, Checks: []Check{{Name: "etcd"}}}),
			want:     Red,
			wantCode: exitcode.Red,
			reasons:  []exitcode.Reason{exitcode.ReadyzFailed},
		},
		{
			name:     "network unavailable is degraded",
			obs:      observed(nodesOf(Node{Name: "node-a", Ready: true, NetworkUnavailable: true}), readyOK()),
			want:     Yellow,
			wantCode: exitcode.Yellow,
			reasons:  []exitcode.Reason{exitcode.NetworkUnavailable},
		},
		{
			name:     "readyz failure outranks a degraded node",
			obs:      observed(nodesOf(Node{Name: "node-a", Ready: true}, Node{Name: "node-b"}), &Readyz{Mode: ModeAggregate}),
			want:     Red,
			wantCode: exitcode.Red,
			reasons:  []exitcode.Reason{exitcode.ReadyzFailed, exitcode.NodeNotReady},
		},
		{
			name: "the item list is the truth, not the summary count",
			obs: observed(&Nodes{
				Total: 2,
				Ready: 2, // a wrong summary must not hide a node that is not ready
				Items: []Node{{Name: "node-a", Ready: true}, {Name: "node-b"}},
			}, readyOK()),
			want:     Yellow,
			wantCode: exitcode.Yellow,
			reasons:  []exitcode.Reason{exitcode.NodeNotReady},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := Build(tc.obs, time.Now())
			if !doc.Observed {
				t.Fatal("document is unobserved")
			}
			if doc.Severity != tc.want {
				t.Fatalf("severity = %s, want %s", doc.Severity, tc.want)
			}
			if got := doc.ExitCode(); got != tc.wantCode {
				t.Fatalf("exit code = %d, want %d", got, tc.wantCode)
			}
			if len(doc.Reasons) != len(tc.reasons) {
				t.Fatalf("reasons = %v, want %v", doc.Reasons, tc.reasons)
			}
			for i, want := range tc.reasons {
				if doc.Reasons[i] != want {
					t.Fatalf("reasons = %v, want %v", doc.Reasons, tc.reasons)
				}
			}
			if doc.Error != nil {
				t.Fatalf("observed document carries an error: %+v", doc.Error)
			}
		})
	}
}

func TestBuildExpectedDark(t *testing.T) {
	down := Observation{
		Cluster:   Cluster{Alias: "lab-one", Expected: Dark},
		Apiserver: Apiserver{Result: Down},
	}
	doc := Build(down, time.Now())
	if !doc.Observed || doc.Severity != Green {
		t.Fatalf("observed = %v severity = %s, want a GREEN observation", doc.Observed, doc.Severity)
	}
	if len(doc.Reasons) != 1 || doc.Reasons[0] != exitcode.ExpectedDark {
		t.Fatalf("reasons = %v", doc.Reasons)
	}
	if got := doc.ExitCode(); got != exitcode.Green {
		t.Fatalf("exit code = %d, want 0", got)
	}

	down.Cluster.Expected = Light
	doc = Build(down, time.Now())
	if !doc.Observed || doc.Severity != Red {
		t.Fatalf("observed = %v severity = %s, want a RED observation", doc.Observed, doc.Severity)
	}
	if len(doc.Reasons) != 1 || doc.Reasons[0] != exitcode.ApiserverDown {
		t.Fatalf("reasons = %v", doc.Reasons)
	}
	if got := doc.ExitCode(); got != exitcode.Red {
		t.Fatalf("exit code = %d, want 2", got)
	}
}

func TestBuildUnobservedOmitsSeverity(t *testing.T) {
	cases := []struct {
		name     string
		failure  *exitcode.Error
		result   Result
		wantCode exitcode.Code
	}{
		{name: "auth", failure: exitcode.New(exitcode.AuthRejected, "the apiserver rejected the credential"), result: Up, wantCode: exitcode.Auth},
		{name: "rbac", failure: exitcode.New(exitcode.RBACDenied, "grant get,list on nodes"), result: Up, wantCode: exitcode.Auth},
		{name: "tls", failure: exitcode.New(exitcode.TLSVerifyFailed, "did not verify"), result: Up, wantCode: exitcode.TLS},
		{name: "silent", failure: exitcode.New(exitcode.HTTPUnresponsive, "no answer"), result: Unknown, wantCode: exitcode.Transport},
		{name: "config", failure: exitcode.New(exitcode.ExecAuthRefused, "exec is refused"), result: Up, wantCode: exitcode.Config},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obs := Observation{
				Cluster:   Cluster{Alias: "lab-one", Expected: Light},
				Apiserver: Apiserver{Result: tc.result},
				Readyz:    readyOK(), // partial evidence must survive
				Failure:   tc.failure,
			}
			doc := Build(obs, time.Now())
			if doc.Observed {
				t.Fatal("document claims to be observed")
			}
			if doc.Severity != "" {
				t.Fatalf("severity = %q, want it absent", doc.Severity)
			}
			if doc.Error == nil || doc.Error.Code != tc.failure.Reason {
				t.Fatalf("error = %+v", doc.Error)
			}
			if len(doc.Reasons) != 1 || doc.Reasons[0] != tc.failure.Reason {
				t.Fatalf("reasons = %v", doc.Reasons)
			}
			if doc.Readyz == nil {
				t.Fatal("partial evidence was dropped")
			}
			if got := doc.ExitCode(); got != tc.wantCode {
				t.Fatalf("exit code = %d, want %d", got, tc.wantCode)
			}
		})
	}
}

func TestBuildUnknownWithoutCauseIsUnobserved(t *testing.T) {
	doc := Build(Observation{
		Cluster:   Cluster{Alias: "lab-one", Expected: Light},
		Apiserver: Apiserver{Result: Unknown},
	}, time.Now())

	if doc.Observed || doc.Severity != "" {
		t.Fatalf("observed = %v severity = %q", doc.Observed, doc.Severity)
	}
	if doc.Error == nil || doc.Error.Code != exitcode.HTTPUnresponsive {
		t.Fatalf("error = %+v", doc.Error)
	}
	if got := doc.ExitCode(); got != exitcode.Transport {
		t.Fatalf("exit code = %d, want 6", got)
	}
}

func TestBuildClampsErrorMessage(t *testing.T) {
	long := strings.Repeat("x", maxErrorLen+50)
	doc := Build(Observation{
		Cluster:   Cluster{Alias: "lab-one", Expected: Light},
		Apiserver: Apiserver{Result: Up},
		Failure:   exitcode.New(exitcode.InternalError, "%s", long),
	}, time.Now())

	if doc.Error == nil || len(doc.Error.Message) != maxErrorLen {
		t.Fatalf("message length = %d, want %d", len(doc.Error.Message), maxErrorLen)
	}
}

func TestBuildTimestampIsUTCSeconds(t *testing.T) {
	doc := Build(observed(nodesOf(Node{Name: "node-a", Ready: true}), readyOK()), time.Date(2026, 11, 13, 15, 4, 5, 987654321, time.FixedZone("x", 3600)))
	if got := doc.GeneratedAt.UTC().Format(timestampLayout); got != "2026-11-13T14:04:05Z" {
		t.Fatalf("generated_at = %s", got)
	}
}
