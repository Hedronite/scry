package report

import (
	"strings"
	"time"

	"github.com/Hedronite/scry/internal/exitcode"
)

// maxErrorLen bounds the message carried in Document.Error (schema maxLength).
const maxErrorLen = 400

// Node reasons that mean "no verdict", not "not ready". The node controller sets
// NodeStatusUnknown when a kubelet stops reporting; kubelet itself only ever
// writes True or False. The schema carries readiness as one bool plus the reason,
// so these two stable reasons are how "unknown" survives that shape.
const (
	reasonNodeStatusUnknown      = "NodeStatusUnknown"
	reasonNodeStatusNeverUpdated = "NodeStatusNeverUpdated"
)

// Build turns raw evidence into a scry.status/v0 document. Severity is absent
// whenever the cluster could not be observed, so no consumer can ingest a false
// RED from a config, auth or TLS failure.
func Build(obs Observation, now time.Time) Document {
	doc := Document{
		Schema:      SchemaV0,
		GeneratedAt: Timestamp{now},
		Cluster:     obs.Cluster,
		Server:      obs.Server,
		Apiserver:   obs.Apiserver,
		Readyz:      obs.Readyz,
		Nodes:       obs.Nodes,
	}

	failure := obs.Failure
	if failure == nil && obs.Apiserver.Result == Unknown {
		// Defensive: an unknown result without a typed cause would otherwise
		// become an observed document with no evidence behind it.
		failure = exitcode.New(exitcode.HTTPUnresponsive,
			"the apiserver connected but gave no usable answer")
	}
	if failure != nil {
		doc.Observed = false
		doc.Error = &ErrorBody{Code: failure.Reason, Message: clampMessage(failure.Message)}
		doc.Reasons = []exitcode.Reason{failure.Reason}
		return doc
	}

	doc.Observed = true
	doc.Severity, doc.Reasons = verdict(obs)
	return doc
}

// verdict applies the expected-dark table and the readiness rules (spec 5).
func verdict(obs Observation) (Severity, []exitcode.Reason) {
	if obs.Apiserver.Result == Down {
		if obs.Cluster.Expected == Dark {
			// Dark on purpose is a normal state, not an error.
			return Green, []exitcode.Reason{exitcode.ExpectedDark}
		}
		return Red, []exitcode.Reason{exitcode.ApiserverDown}
	}

	severity := Green
	var reasons []exitcode.Reason
	worse := func(s Severity, r exitcode.Reason) {
		if rank(s) > rank(severity) {
			severity = s
		}
		for _, seen := range reasons {
			if seen == r {
				return
			}
		}
		reasons = append(reasons, r)
	}

	if obs.Readyz != nil && !obs.Readyz.OK {
		worse(Red, exitcode.ReadyzFailed)
	}
	if obs.Nodes != nil {
		nodes := *obs.Nodes
		switch nodes.Total {
		case 0:
			worse(Red, exitcode.NoNodes)
		default:
			notReady, unknown := 0, 0
			for _, node := range nodes.Items {
				if node.Ready {
					continue
				}
				if nodeStatusUnknown(node) {
					unknown++
					continue
				}
				notReady++
			}
			switch {
			case notReady > 0 && nodes.Ready == 0:
				worse(Red, exitcode.NodeNotReady)
			case notReady > 0:
				worse(Yellow, exitcode.NodeNotReady)
			case unknown > 0:
				// A node that stopped reporting is degraded, not failed.
				worse(Yellow, exitcode.NodeNotReady)
			default:
			}
			for _, node := range nodes.Items {
				if node.NetworkUnavailable {
					worse(Yellow, exitcode.NetworkUnavailable)
					break
				}
			}
		}
	}
	return severity, reasons
}

// nodeStatusUnknown reports a Ready condition the server called Unknown.
func nodeStatusUnknown(node Node) bool {
	return node.Reason == reasonNodeStatusUnknown || node.Reason == reasonNodeStatusNeverUpdated
}

func rank(s Severity) int {
	switch s {
	case Green:
		return 0
	case Yellow:
		return 1
	case Red:
		return 2
	default:
		return -1
	}
}

// clampMessage bounds a printable message for the schema.
func clampMessage(message string) string {
	cleaned := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, message)
	cleaned = strings.TrimSpace(cleaned)
	if len(cleaned) > maxErrorLen {
		cleaned = cleaned[:maxErrorLen]
	}
	return cleaned
}
