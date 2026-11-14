package main_test

// Process-level performance measurements for R7: the real binary's wall time and
// peak RSS on one status run against a loopback TLS fixture, plus the binary size
// of every release target.
//
// Only the wall-time budget is an assertion. RSS and size are recorded with
// t.Logf so a runner difference shows up as a number to read, never as a red
// build. The binary itself is built once by TestMain in process_test.go; this
// file reuses that binary and adds no TestMain of its own.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/Hedronite/scry/internal/fakecluster"
)

// perfBudget is the R7 wall-time budget for one status run.
const perfBudget = 200 * time.Millisecond

// perfTargets are the release targets (R2).
var perfTargets = []struct {
	goos   string
	goarch string
}{
	{goos: "linux", goarch: "amd64"},
	{goos: "linux", goarch: "arm64"},
	{goos: "darwin", goarch: "arm64"},
}

// perfSample is one measured run.
type perfSample struct {
	elapsed time.Duration
	exit    int
	rss     int64 // bytes
}

// perfKubeconfig writes a kubeconfig for the fixture with no inherited state.
func perfKubeconfig(t *testing.T, server *fakecluster.Server) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if _, err := server.Kubeconfig(path, nil); err != nil {
		t.Fatalf("kubeconfig: %v", err)
	}
	return path
}

// perfRun executes the binary once and records wall time and peak RSS.
func perfRun(t *testing.T, args ...string) perfSample {
	t.Helper()
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = t.TempDir()
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"TMPDIR=" + t.TempDir(),
	}
	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start)
	if ctx.Err() != nil {
		t.Fatalf("scry %v timed out: %v", args, ctx.Err())
	}
	sample := perfSample{elapsed: elapsed}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		sample.exit = exitErr.ExitCode()
	default:
		t.Fatalf("run scry %v: %v", args, err)
	}
	if cmd.ProcessState != nil {
		sample.rss = peakRSS(cmd.ProcessState.SysUsage())
	}
	return sample
}

// peakRSS normalises Rusage.Maxrss: kilobytes on linux, bytes on darwin.
func peakRSS(usage any) int64 {
	rusage, ok := usage.(*syscall.Rusage)
	if !ok || rusage == nil {
		return 0
	}
	switch runtime.GOOS {
	case "linux":
		return int64(rusage.Maxrss) * 1024
	default:
		return int64(rusage.Maxrss)
	}
}

func TestStatusProcessBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("process measurements are skipped with -short")
	}
	server, err := fakecluster.New(fakecluster.Options{})
	if err != nil {
		t.Fatalf("fake cluster: %v", err)
	}
	t.Cleanup(server.Close)
	kubeconfig := perfKubeconfig(t, server)
	args := []string{"status", "--kubeconfig", kubeconfig, "--json"}

	// One warm-up run so first-touch page faults are not charged to the budget.
	if sample := perfRun(t, args...); sample.exit != 0 {
		t.Fatalf("warm-up exited %d", sample.exit)
	}

	const runs = 5
	samples := make([]perfSample, 0, runs)
	for range runs {
		sample := perfRun(t, args...)
		if sample.exit != 0 {
			t.Fatalf("status exited %d", sample.exit)
		}
		samples = append(samples, sample)
	}
	elapsed := make([]time.Duration, 0, runs)
	rss := make([]int64, 0, runs)
	for _, sample := range samples {
		elapsed = append(elapsed, sample.elapsed)
		rss = append(rss, sample.rss)
	}
	slices.Sort(elapsed)
	median := elapsed[len(elapsed)/2]
	t.Logf("status process: median %s, min %s, max %s (n=%d)",
		median, elapsed[0], elapsed[len(elapsed)-1], runs)
	t.Logf("peak RSS: %s", humanBytes(slices.Max(rss)))
	if median > perfBudget {
		t.Fatalf("median status process run took %s, over the %s budget", median, perfBudget)
	}
}

func TestBinarySizePerTarget(t *testing.T) {
	if testing.Short() {
		t.Skip("cross-compiles are skipped with -short")
	}
	dir := t.TempDir()
	for _, target := range perfTargets {
		name := "scry_" + target.goos + "_" + target.goarch
		out := filepath.Join(dir, name)
		args := []string{
			"build", "-trimpath",
			"-ldflags=-s -w -buildid=",
			"-o", out, "./cmd/scry",
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		cmd := exec.CommandContext(ctx, "go", args...)
		cmd.Dir = repoRoot(t)
		cmd.Env = append(os.Environ(),
			"CGO_ENABLED=0",
			"GOOS="+target.goos,
			"GOARCH="+target.goarch,
		)
		output, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("build %s: %v: %s", name, err, output)
		}
		info, err := os.Stat(out)
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		t.Logf("binary size %s/%s: %s (%s bytes)",
			target.goos, target.goarch, humanBytes(info.Size()), strconv.FormatInt(info.Size(), 10))
	}
}

// repoRoot walks up from the test's working directory to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test directory")
		}
		dir = parent
	}
}

// humanBytes renders a byte count for a log line.
func humanBytes(size int64) string {
	const unit = 1024
	if size < unit {
		return strconv.FormatInt(size, 10) + " B"
	}
	value := float64(size)
	for _, suffix := range []string{"KiB", "MiB", "GiB"} {
		value /= unit
		if value < unit {
			return strconv.FormatFloat(value, 'f', 1, 64) + " " + suffix
		}
	}
	return strconv.FormatFloat(value, 'f', 1, 64) + " TiB"
}
