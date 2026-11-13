package kube

import (
	"strings"

	"github.com/Hedronite/scry/internal/report"
)

const (
	maxChecks    = 256
	maxCheckName = 128
)

// parseReadyz turns a /readyz?verbose answer into the document's readyz block.
//
// Three servers matter and only one of them is stock Kubernetes:
//   - stock kube-apiserver answers verbose lines ("[+]etcd ok") and a summary;
//   - h3s answers a bare "ok" for any status and ignores the query entirely;
//   - anything unrecognised still has an HTTP status, which is the fail-safe.
//
// An unrecognised body is never echoed: it is server-controlled text and could
// carry paths or addresses. A failing "[+]/-" line outranks a 2xx status, because
// the server has told us something failed; only the aggregate form falls back to
// the status alone.
func parseReadyz(status int, body []byte) report.Readyz {
	succeeded := status >= 200 && status < 300
	readyz := report.Readyz{Mode: report.ModeAggregate, Checks: []report.Check{}}

	sawCheck := false
	allChecksOK := true
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		parsed, ok := parseReadyzLine(line)
		if !ok {
			continue
		}
		if parsed.summary {
			// A summary line only corroborates sub-checks that are actually
			// present; on its own it cannot make this endpoint look verbose.
			if sawCheck {
				allChecksOK = allChecksOK && parsed.ok
			}
			continue
		}
		sawCheck = true
		allChecksOK = allChecksOK && parsed.ok
		name := clampString(parsed.name, maxCheckName)
		if name == "" || len(readyz.Checks) >= maxChecks {
			// A name that is only control characters has nothing to report, and
			// the schema requires a non-empty one.
			continue
		}
		readyz.Checks = append(readyz.Checks, report.Check{
			Name:    name,
			OK:      parsed.ok,
			Message: clampString(parsed.message, report.MaxMessageLen),
		})
	}

	readyz.OK = succeeded
	if sawCheck {
		readyz.Mode = report.ModeVerbose
		readyz.OK = succeeded && allChecksOK
	}
	return readyz
}

type readyzLine struct {
	name    string
	message string
	ok      bool
	summary bool
}

// parseReadyzLine recognises the verbose forms and the trailing summary. A line
// it does not recognise is ignored, not guessed at.
func parseReadyzLine(line string) (readyzLine, bool) {
	switch {
	case strings.HasPrefix(line, "[+]"):
		name, message := splitCheck(line[3:])
		if name == "" {
			// "[+]" with no name is not a check; the schema requires one.
			return readyzLine{}, false
		}
		return readyzLine{name: name, message: message, ok: true}, true
	case strings.HasPrefix(line, "[-]"):
		name, message := splitCheck(line[3:])
		if name == "" {
			return readyzLine{}, false
		}
		return readyzLine{name: name, message: message}, true
	case line == "readyz check passed":
		return readyzLine{ok: true, summary: true}, true
	case line == "readyz check failed":
		return readyzLine{summary: true}, true
	default:
		return readyzLine{}, false
	}
}

// splitCheck reads "<name> ok", "<name> failed" or "<name> failed: <message>".
func splitCheck(rest string) (name, message string) {
	rest = strings.TrimSpace(rest)
	name, tail, _ := strings.Cut(rest, " ")
	tail = strings.TrimSpace(tail)
	if after, found := strings.CutPrefix(tail, "failed:"); found {
		return name, strings.TrimSpace(after)
	}
	return name, ""
}

// clampString bounds a server-supplied string for the schema, and drops control
// characters so a value cannot rewrite a terminal line.
func clampString(s string, limit int) string {
	cleaned := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	cleaned = strings.TrimSpace(cleaned)
	if len(cleaned) > limit {
		cleaned = cleaned[:limit]
	}
	return cleaned
}
