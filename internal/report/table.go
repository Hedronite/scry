package report

import (
	"fmt"
	"strings"
	"time"
)

// col is the width of the label column; sub-rows indent by the same amount.
const col = 12

// Render returns d as a plain-text table for a terminal, without a trailing
// newline. It is a pure function of the document: no I/O, no clock, no
// colour. Rows are "label: text"; sub-checks and nodes are indented under
// their headline row. Consumers that need stable fields should read the JSON
// form instead.
func Render(d Document) string {
	var b strings.Builder
	row(&b, "cluster", fmt.Sprintf("%s (expected %s)", d.Cluster.Alias, d.Cluster.Expected))
	row(&b, "as of", d.GeneratedAt.UTC().Format(timestampLayout))
	if d.Server != nil {
		row(&b, "server", fmt.Sprintf("%s (%s)", d.Server.Version, d.Server.Flavor))
	}
	row(&b, "apiserver", apiserverLine(d))
	if d.Readyz != nil {
		row(&b, "readyz", readyzLine(*d.Readyz))
		for _, c := range d.Readyz.Checks {
			status := "ok"
			if !c.OK {
				status = "FAIL"
			}
			line := fmt.Sprintf("%-6s%s", status, c.Name)
			if c.Message != "" {
				line += ": " + c.Message
			}
			ind(&b, line)
		}
	}
	if d.Nodes != nil {
		n := *d.Nodes
		headline := fmt.Sprintf("%d of %d ready", n.Ready, n.Total)
		if n.Total == 0 {
			headline = "no nodes reported"
		}
		row(&b, "nodes", headline)
		for _, it := range n.Items {
			status := "ready"
			if !it.Ready {
				status = "NOT READY"
			}
			line := fmt.Sprintf("%-10s%s", status, it.Name)
			var extra []string
			if it.Reason != "" {
				extra = append(extra, it.Reason)
			}
			if it.NetworkUnavailable {
				extra = append(extra, "network unavailable")
			}
			if len(extra) > 0 {
				line += " (" + strings.Join(extra, ", ") + ")"
			}
			ind(&b, line)
		}
	}
	switch {
	case d.Observed:
		row(&b, "severity", string(d.Severity))
	case d.Error != nil:
		row(&b, "error", fmt.Sprintf("%s: %s", d.Error.Code, d.Error.Message))
	default:
		row(&b, "error", "(no error body)")
	}
	if len(d.Reasons) > 0 {
		rs := make([]string, len(d.Reasons))
		for i, r := range d.Reasons {
			rs[i] = string(r)
		}
		row(&b, "reasons", strings.Join(rs, ", "))
	}
	if d.Wait != nil {
		dur := time.Duration(d.Wait.ElapsedMS) * time.Millisecond
		row(&b, "wait", fmt.Sprintf("%s after %d polls (%s)", d.Wait.Outcome, d.Wait.Polls, dur))
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func row(b *strings.Builder, label, text string) {
	fmt.Fprintf(b, "%-*s%s\n", col, label+":", text)
}

func ind(b *strings.Builder, text string) {
	fmt.Fprintf(b, "%-*s%s\n", col, "", text)
}

// apiserverLine composes the apiserver row: the wire verdict, then the
// declared-dark annotation, then the observable extras.
func apiserverLine(d Document) string {
	parts := []string{string(d.Apiserver.Result)}
	if d.Apiserver.Result == Down && d.Cluster.Expected == Dark {
		parts = append(parts, "expected dark")
	}
	if d.Apiserver.HTTPStatus != nil {
		parts = append(parts, fmt.Sprintf("http %d", *d.Apiserver.HTTPStatus))
	}
	if d.Apiserver.LatencyMS != nil {
		parts = append(parts, fmt.Sprintf("%dms", *d.Apiserver.LatencyMS))
	}
	if d.Apiserver.Livez != "" {
		parts = append(parts, "livez "+d.Apiserver.Livez)
	}
	return strings.Join(parts, ", ")
}

// readyzLine composes the readyz headline. An aggregate report proves only
// that the server answered; the table says so instead of inventing detail.
func readyzLine(r Readyz) string {
	line := string(r.Mode)
	if r.OK {
		line += ", ok"
	} else {
		line += ", FAILED"
	}
	switch {
	case r.Mode == ModeAggregate:
		line += " (no sub-checks reported)"
	case !r.OK:
		failed := 0
		for _, c := range r.Checks {
			if !c.OK {
				failed++
			}
		}
		line += fmt.Sprintf(", %d of %d checks failed", failed, len(r.Checks))
	}
	return line
}
