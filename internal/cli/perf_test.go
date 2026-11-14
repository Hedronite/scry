package cli

import (
	"bytes"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/Hedronite/scry/internal/fakecluster"
)

// The two budgets of R7, measured through the real command body rather than a
// stub observer: a status run against a loopback TLS fixture must finish well
// inside 200 ms, and it must leave no goroutine behind.
const statusBudget = 200 * time.Millisecond

func perfFixture(t *testing.T) string {
	t.Helper()
	server, err := fakecluster.New(fakecluster.Options{})
	if err != nil {
		t.Fatalf("fake cluster: %v", err)
	}
	t.Cleanup(server.Close)
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if _, err := server.Kubeconfig(path, nil); err != nil {
		t.Fatalf("kubeconfig: %v", err)
	}
	return path
}

// perfStatus runs one status command and returns how long it took.
func perfStatus(t *testing.T, kubeconfig string) time.Duration {
	t.Helper()
	var out, errOut bytes.Buffer
	args := []string{"status", "--kubeconfig", kubeconfig, "--json"}
	start := time.Now()
	code := Execute(args, Streams{Out: &out, Err: &errOut})
	elapsed := time.Since(start)
	if code != 0 {
		t.Fatalf("status exited %d: %s", code, errOut.String())
	}
	if out.Len() == 0 {
		t.Fatal("status wrote no document")
	}
	return elapsed
}

func median(durations []time.Duration) time.Duration {
	ordered := slices.Clone(durations)
	slices.Sort(ordered)
	return ordered[len(ordered)/2]
}

func TestStatusStaysUnderBudget(t *testing.T) {
	kubeconfig := perfFixture(t)
	// One warm-up run so process-wide one-time setup is not charged to the budget.
	perfStatus(t, kubeconfig)

	const runs = 7
	durations := make([]time.Duration, 0, runs)
	for range runs {
		durations = append(durations, perfStatus(t, kubeconfig))
	}
	got := median(durations)
	t.Logf("status against the loopback fixture: median %s (min %s, max %s, n=%d)",
		got, slices.Min(durations), slices.Max(durations), runs)
	if got > statusBudget {
		t.Fatalf("median status run took %s, over the %s budget", got, statusBudget)
	}
}

func TestStatusLeavesNoGoroutineBehind(t *testing.T) {
	kubeconfig := perfFixture(t)
	// A warm-up run lets the TLS stack and the fixture settle before the
	// baseline is taken; the budget is about what status itself leaves running.
	perfStatus(t, kubeconfig)

	baseline := runtime.NumGoroutine()
	const runs = 5
	for range runs {
		perfStatus(t, kubeconfig)
	}

	// Connection goroutines retire asynchronously, so poll briefly rather than
	// demanding an instant return; anything that outlives this window is a leak.
	deadline := time.Now().Add(2 * time.Second)
	for {
		after := runtime.NumGoroutine()
		if after <= baseline {
			t.Logf("goroutines: baseline %d, after %d runs %d", baseline, runs, after)
			return
		}
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			n := runtime.Stack(buf, true)
			t.Fatalf("goroutines: baseline %d, after %d runs %d\n%s", baseline, runs, after, buf[:n])
		}
		time.Sleep(10 * time.Millisecond)
	}
}
