// Package wait polls an Observer until a cluster is ready or a deadline
// passes (spec section 6). It is pure over an Observer and a Clock, so the
// whole schedule is testable without a network or a real clock. Every poll is
// an independent observation: never a watch, never a resumption token.
package wait

import (
	"context"
	"time"

	"github.com/Hedronite/scry/internal/exitcode"
	"github.com/Hedronite/scry/internal/report"
)

// Backoff schedule: 500 ms, then x1.5 per poll, capped at 5 s, no jitter.
const (
	InitialDelay   = 500 * time.Millisecond
	BackoffFactor  = 1.5
	MaxDelay       = 5 * time.Second
	DefaultTimeout = 60 * time.Second
)

// Observer takes one observation of one cluster.
type Observer interface {
	Observe(ctx context.Context) report.Observation
}

// Clock is the time seam. Sleep returns early with ctx's error if ctx ends.
type Clock interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration) error
}

// RealClock is the wall clock.
type RealClock struct{}

// Now implements Clock.
func (RealClock) Now() time.Time { return time.Now() }

// Sleep implements Clock.
func (RealClock) Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Refusal is the document for a wait on a declared-dark cluster: nothing can
// make a documented-dark apiserver pass readyz, so Scry says so and makes no
// request (exit 3, wait_expected_dark).
func Refusal(cl report.Cluster, now time.Time) report.Document {
	return report.Build(report.Observation{
		Cluster:   cl,
		Apiserver: report.Apiserver{Result: report.Unknown},
		Failure: exitcode.New(exitcode.WaitExpectedDark,
			"cluster %q is declared dark; wait cannot succeed: pass --expected light if it is meant to come up", cl.Alias),
	}, now)
}

// Ready reports whether a poll satisfies --for=readyz: observed, readyz
// passing, apiserver up.
func Ready(d report.Document) bool {
	return d.Observed && d.Apiserver.Result == report.Up && d.Readyz != nil && d.Readyz.OK
}

// failFast reports whether waiting longer cannot help: the failure is about
// config, credentials, TLS or Scry itself, not about the cluster coming up.
func failFast(d report.Document) bool {
	if d.Observed || d.Error == nil {
		return false
	}
	c, ok := exitcode.ForReason(d.Error.Code)
	if !ok {
		return false
	}
	switch c {
	case exitcode.Config, exitcode.Auth, exitcode.TLS, exitcode.Internal:
		return true
	case exitcode.Green, exitcode.Yellow, exitcode.Red, exitcode.Transport, exitcode.Usage:
		return false
	}
	return false
}

// Delay returns the sleep before poll n+1 (n starts at 1 for the first poll).
func Delay(n int) time.Duration {
	d := float64(InitialDelay)
	for i := 1; i < n; i++ {
		d *= BackoffFactor
		if d >= float64(MaxDelay) {
			return MaxDelay
		}
	}
	return time.Duration(d)
}

// Run polls until the cluster is ready, a fail-fast failure, or the timeout.
// It returns the final document: a ready or fail-fast document is the last
// poll's; a timeout is the last poll's with the timeout verdict added (reason
// first, wait block, exit 2). Run makes at least one poll.
func Run(ctx context.Context, timeout time.Duration, o Observer, clk Clock) report.Document {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	start := clk.Now()
	deadline := start.Add(timeout)
	// The context bounds in-flight requests in real time; the loop itself
	// decides by the Clock, so tests can drive a fake one.
	pollCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var last report.Document
	for n := 1; ; n++ {
		last = report.Build(o.Observe(pollCtx), clk.Now())
		if Ready(last) {
			last.Wait = &report.Wait{Outcome: report.WaitReady, Polls: n, ElapsedMS: clk.Now().Sub(start).Milliseconds()}
			return last
		}
		if failFast(last) {
			return last
		}
		remaining := deadline.Sub(clk.Now())
		if remaining <= 0 || pollCtx.Err() != nil {
			return timedOut(last, n, clk.Now().Sub(start))
		}
		d := Delay(n)
		if d > remaining {
			d = remaining
		}
		if err := clk.Sleep(pollCtx, d); err != nil {
			return timedOut(last, n, clk.Now().Sub(start))
		}
	}
}

func timedOut(last report.Document, polls int, elapsed time.Duration) report.Document {
	reasons := []exitcode.Reason{exitcode.Timeout}
	for _, r := range last.Reasons {
		if r != exitcode.Timeout {
			reasons = append(reasons, r)
		}
	}
	last.Reasons = reasons
	last.Wait = &report.Wait{Outcome: report.WaitTimeout, Polls: polls, ElapsedMS: elapsed.Milliseconds()}
	return last
}
