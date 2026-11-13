package wait

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/Hedronite/scry/internal/exitcode"
	"github.com/Hedronite/scry/internal/report"
)

// fakeClock advances only when Sleep is called.
type fakeClock struct {
	now    time.Time
	sleeps []time.Duration
}

func (c *fakeClock) Now() time.Time { return c.now }
func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.sleeps = append(c.sleeps, d)
	c.now = c.now.Add(d)
	return nil
}

// script returns one observation per poll, repeating the last.
type script struct {
	obs   []report.Observation
	polls int
}

func (s *script) Observe(context.Context) report.Observation {
	i := min(s.polls, len(s.obs)-1)
	s.polls++
	return s.obs[i]
}

var cl = report.Cluster{Alias: "lab", Expected: report.Light}

func ptr[T any](v T) *T { return &v }

func ready(nodesReady, total int) report.Observation {
	items := make([]report.Node, total)
	for i := range items {
		items[i] = report.Node{Name: "node-" + string(rune('a'+i)), Ready: i < nodesReady}
	}
	return report.Observation{
		Cluster:   cl,
		Apiserver: report.Apiserver{Result: report.Up, HTTPStatus: ptr(200)},
		Readyz:    &report.Readyz{Mode: report.ModeAggregate, OK: true, Checks: []report.Check{}},
		Nodes:     &report.Nodes{Total: total, Ready: nodesReady, Items: items},
	}
}

func notReady() report.Observation {
	o := ready(1, 1)
	o.Readyz.OK = false
	return o
}

func down() report.Observation {
	return report.Observation{Cluster: cl, Apiserver: report.Apiserver{Result: report.Down}}
}

func failed(r exitcode.Reason) report.Observation {
	return report.Observation{Cluster: cl, Apiserver: report.Apiserver{Result: report.Up},
		Failure: exitcode.New(r, "synthetic failure")}
}

func run(t *testing.T, timeout time.Duration, s *script) (report.Document, *fakeClock) {
	t.Helper()
	clk := &fakeClock{now: time.Date(2026, 11, 13, 20, 0, 0, 0, time.UTC)}
	return Run(context.Background(), timeout, s, clk), clk
}

func TestReadyOnFirstPollMakesOnePollAndNoSleep(t *testing.T) {
	s := &script{obs: []report.Observation{ready(2, 2)}}
	d, clk := run(t, time.Minute, s)
	if s.polls != 1 || len(clk.sleeps) != 0 {
		t.Fatalf("polls=%d sleeps=%v", s.polls, clk.sleeps)
	}
	if d.Wait == nil || d.Wait.Outcome != report.WaitReady || d.Wait.Polls != 1 {
		t.Fatalf("wait = %+v", d.Wait)
	}
	if d.ExitCode() != exitcode.Green {
		t.Fatalf("exit = %d", d.ExitCode())
	}
}

func TestReadyButDegradedExitsOne(t *testing.T) {
	d, _ := run(t, time.Minute, &script{obs: []report.Observation{ready(1, 2)}})
	if d.Wait == nil || d.Wait.Outcome != report.WaitReady || d.ExitCode() != exitcode.Yellow {
		t.Fatalf("wait=%+v exit=%d", d.Wait, d.ExitCode())
	}
}

func TestBackoffScheduleIsExactAndCapped(t *testing.T) {
	obs := make([]report.Observation, 0, 12)
	for range 11 {
		obs = append(obs, notReady())
	}
	obs = append(obs, ready(1, 1))
	s := &script{obs: obs}
	d, clk := run(t, time.Hour, s)
	want := []time.Duration{
		500 * time.Millisecond, 750 * time.Millisecond, 1125 * time.Millisecond,
		1687500 * time.Microsecond, 2531250 * time.Microsecond, 3796875 * time.Microsecond,
		5 * time.Second, 5 * time.Second, 5 * time.Second, 5 * time.Second, 5 * time.Second,
	}
	if !slices.Equal(clk.sleeps, want) {
		t.Fatalf("sleeps = %v\nwant     %v", clk.sleeps, want)
	}
	if d.Wait.Polls != 12 || d.Wait.Outcome != report.WaitReady {
		t.Fatalf("wait = %+v", d.Wait)
	}
}

func TestTimeoutIsExitTwoWithTimeoutFirst(t *testing.T) {
	s := &script{obs: []report.Observation{notReady()}}
	d, clk := run(t, 3*time.Second, s)
	if d.Wait == nil || d.Wait.Outcome != report.WaitTimeout {
		t.Fatalf("wait = %+v", d.Wait)
	}
	if len(d.Reasons) == 0 || d.Reasons[0] != exitcode.Timeout {
		t.Fatalf("reasons = %v", d.Reasons)
	}
	if !slices.Contains(d.Reasons, exitcode.ReadyzFailed) {
		t.Fatalf("last poll's reasons lost: %v", d.Reasons)
	}
	if d.ExitCode() != exitcode.Red {
		t.Fatalf("exit = %d", d.ExitCode())
	}
	if got := clk.now.Sub(time.Date(2026, 11, 13, 20, 0, 0, 0, time.UTC)); got > 3*time.Second {
		t.Fatalf("slept past the deadline: %v", got)
	}
	if d.Wait.Polls < 2 {
		t.Fatalf("polls = %d", d.Wait.Polls)
	}
}

func TestTimeoutWithNeverObservedStaysUnobservedButExitsTwo(t *testing.T) {
	d, _ := run(t, 2*time.Second, &script{obs: []report.Observation{failed(exitcode.HTTPUnresponsive)}})
	if d.Observed || d.Wait == nil || d.Wait.Outcome != report.WaitTimeout {
		t.Fatalf("doc = %+v", d)
	}
	if d.ExitCode() != exitcode.Red {
		t.Fatalf("exit = %d", d.ExitCode())
	}
}

func TestDarkApiserverKeepsPollingUntilItAnswers(t *testing.T) {
	s := &script{obs: []report.Observation{down(), down(), ready(1, 1)}}
	d, _ := run(t, time.Minute, s)
	if d.Wait == nil || d.Wait.Outcome != report.WaitReady || d.Wait.Polls != 3 {
		t.Fatalf("wait = %+v", d.Wait)
	}
}

func TestFailFastOnConfigAuthTLSInternal(t *testing.T) {
	cases := map[exitcode.Reason]exitcode.Code{
		exitcode.KubeconfigInvalid: exitcode.Config,
		exitcode.AuthRejected:      exitcode.Auth,
		exitcode.RBACDenied:        exitcode.Auth,
		exitcode.TLSVerifyFailed:   exitcode.TLS,
		exitcode.AllowlistDenied:   exitcode.Internal,
	}
	for r, code := range cases {
		s := &script{obs: []report.Observation{failed(r)}}
		d, clk := run(t, time.Minute, s)
		if s.polls != 1 || len(clk.sleeps) != 0 {
			t.Errorf("%s: polls=%d sleeps=%v, want fail-fast", r, s.polls, clk.sleeps)
		}
		if d.Wait != nil {
			t.Errorf("%s: fail-fast document carries a wait block", r)
		}
		if d.ExitCode() != code {
			t.Errorf("%s: exit = %d, want %d", r, d.ExitCode(), code)
		}
	}
}

func TestSilentPeerIsRetriedNotFailFast(t *testing.T) {
	s := &script{obs: []report.Observation{failed(exitcode.HTTPUnresponsive), ready(1, 1)}}
	d, _ := run(t, time.Minute, s)
	if s.polls != 2 || d.Wait == nil || d.Wait.Outcome != report.WaitReady {
		t.Fatalf("polls=%d wait=%+v", s.polls, d.Wait)
	}
}

func TestRefusalOnDeclaredDarkIsExitThreeAndMakesNoObservation(t *testing.T) {
	d := Refusal(report.Cluster{Alias: "lab", Expected: report.Dark}, time.Now())
	if d.Observed || d.Error == nil || d.Error.Code != exitcode.WaitExpectedDark {
		t.Fatalf("doc = %+v", d)
	}
	if d.ExitCode() != exitcode.Config {
		t.Fatalf("exit = %d", d.ExitCode())
	}
	if len(d.Reasons) != 1 || d.Reasons[0] != exitcode.WaitExpectedDark {
		t.Fatalf("reasons = %v", d.Reasons)
	}
}

func TestCancelledContextEndsTheWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	clk := &fakeClock{now: time.Now()}
	d := Run(ctx, time.Minute, &script{obs: []report.Observation{notReady()}}, clk)
	if d.Wait == nil || d.Wait.Outcome != report.WaitTimeout {
		t.Fatalf("wait = %+v", d.Wait)
	}
}

func TestDelayFunction(t *testing.T) {
	if Delay(1) != InitialDelay || Delay(2) != 750*time.Millisecond || Delay(50) != MaxDelay {
		t.Fatalf("delays: %v %v %v", Delay(1), Delay(2), Delay(50))
	}
}
