package outfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A directory fsync failure after the rename leaves the new file in place and
// reports ErrDirSync, so callers can tell "written, durability unconfirmed"
// from "not written". No path or OS text reaches the message.
func TestDirSyncFailureAfterRenameIsTypedAndTheFileIsInPlace(t *testing.T) {
	old := syncDir
	syncDir = func(string) error {
		return &os.PathError{Op: "fsync", Path: "/secret/dir", Err: errors.New("input/output error")}
	}
	t.Cleanup(func() { syncDir = old })

	dir := t.TempDir()
	path := filepath.Join(dir, "status.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Write(path, []byte("new"))
	if !errors.Is(err, ErrDirSync) {
		t.Fatalf("err = %v, want ErrDirSync", err)
	}
	if strings.Contains(err.Error(), "/secret") || strings.Contains(err.Error(), dir) {
		t.Fatalf("error carries a path: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "new" {
		t.Fatalf("file = %q, want the new content in place", got)
	}
	es, _ := os.ReadDir(dir)
	if len(es) != 1 {
		t.Fatalf("directory holds %d entries, want 1 (no temp file)", len(es))
	}
}

// Failures before the rename are not ErrDirSync.
func TestEarlierFailuresAreNotDirSync(t *testing.T) {
	err := Write(filepath.Join(t.TempDir(), "absent", "status.json"), []byte("x"))
	if err == nil || errors.Is(err, ErrDirSync) {
		t.Fatalf("err = %v, want a plain write failure", err)
	}
}
