package outfile_test

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/Hedronite/scry/internal/outfile"
)

func entries(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	return names
}

func TestWriteCreatesFileMode0644AndLeavesNoTemp(t *testing.T) {
	old := syscall.Umask(0o077) // a strict umask must not make the file unreadable
	defer syscall.Umask(old)
	dir := t.TempDir()
	path := filepath.Join(dir, "status.json")
	if err := outfile.Write(path, []byte("one\n")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "one\n" {
		t.Fatalf("read = %q, %v", got, err)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %v, want 0644", st.Mode().Perm())
	}
	if n := entries(t, dir); len(n) != 1 || n[0] != "status.json" {
		t.Fatalf("directory holds %v, want only status.json", n)
	}
}

func TestWriteReplacesAndLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "status.json")
	for _, body := range []string{"first", "second, longer", "3"} {
		if err := outfile.Write(path, []byte(body)); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(path); string(got) != body {
			t.Fatalf("read = %q, want %q", got, body)
		}
	}
	if n := entries(t, dir); len(n) != 1 {
		t.Fatalf("directory holds %v", n)
	}
}

// The temp file must live in the target directory: an unwritable target
// directory fails even though the process TMPDIR is writable.
func TestUnwritableDirectoryFailsEvenWithWritableTMPDIR(t *testing.T) {
	if os.Geteuid() == 0 || runtime.GOOS == "windows" {
		t.Skip("directory permissions do not bind this user")
	}
	t.Setenv("TMPDIR", t.TempDir())
	dir := t.TempDir()
	path := filepath.Join(dir, "status.json")
	if err := outfile.Write(path, []byte("old")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(dir, 0o755) }()

	err := outfile.Write(path, []byte("new"))
	if err == nil {
		t.Fatal("write into a 0555 directory succeeded; the temp file is not in the target directory")
	}
	if strings.Contains(err.Error(), dir) {
		t.Fatalf("error carries the path: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "old" {
		t.Fatalf("old file changed: %q", got)
	}
}

// A rename failure after the temp file was written must remove the temp file
// and leave the target alone: here the target is a non-empty directory.
func TestRenameFailureRemovesTempAndKeepsTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "status.json")
	if err := os.MkdirAll(filepath.Join(target, "keep"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := outfile.Write(target, []byte("new"))
	if err == nil {
		t.Fatal("rename over a directory succeeded")
	}
	if strings.Contains(err.Error(), dir) {
		t.Fatalf("error carries the path: %v", err)
	}
	if n := entries(t, dir); len(n) != 1 || n[0] != "status.json" {
		t.Fatalf("directory holds %v, want only the original target", n)
	}
	if _, err := os.Stat(filepath.Join(target, "keep")); err != nil {
		t.Fatal("target changed")
	}
}

func TestMissingDirectoryFailsWithoutCreatingIt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "absent")
	if err := outfile.Write(filepath.Join(dir, "status.json"), []byte("x")); err == nil {
		t.Fatal("write into a missing directory succeeded")
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("Write created the directory")
	}
}

// A symlink at PATH is replaced, not followed: the link target is untouched.
func TestSymlinkTargetIsReplacedNotFollowed(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.json")
	link := filepath.Join(dir, "status.json")
	if err := os.WriteFile(real, []byte("real"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks unavailable")
	}
	if err := outfile.Write(link, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(real); string(got) != "real" {
		t.Fatalf("link target was written through: %q", got)
	}
	if got, _ := os.ReadFile(link); string(got) != "new" {
		t.Fatalf("link not replaced: %q", got)
	}
}

// A reader looping over PATH while the file is rewritten sees only complete
// documents. The two payloads differ in size and content, so a partial write or
// a truncate-then-write would show up as neither.
func TestReaderNeverSeesAPartialFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "status.json")
	small := []byte(`{"a":"` + strings.Repeat("a", 100) + `"}` + "\n")
	big := []byte(`{"b":"` + strings.Repeat("b", 1<<20) + `"}` + "\n")
	if err := outfile.Write(path, small); err != nil {
		t.Fatal(err)
	}

	var stop atomic.Bool
	var wg sync.WaitGroup
	var bad atomic.Value
	reads := atomic.Int64{}
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				b, err := os.ReadFile(path)
				if err != nil {
					bad.Store("read error: " + err.Error())
					return
				}
				reads.Add(1)
				if !bytes.Equal(b, small) && !bytes.Equal(b, big) {
					bad.Store("partial or empty file: " + string(b[:min(len(b), 40)]))
					return
				}
			}
		}()
	}
	for i := range 60 {
		body := small
		if i%2 == 0 {
			body = big
		}
		if err := outfile.Write(path, body); err != nil {
			t.Fatal(err)
		}
	}
	stop.Store(true)
	wg.Wait()
	if v := bad.Load(); v != nil {
		t.Fatal(v)
	}
	if reads.Load() == 0 {
		t.Fatal("the reader never ran")
	}
	if n := entries(t, dir); len(n) != 1 {
		t.Fatalf("directory holds %v", n)
	}
}
