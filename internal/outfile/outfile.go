// Package outfile publishes a file atomically: a reader of the target path
// sees the previous complete file or the new complete file, never a partial or
// empty one.
//
// Scry uses it for `status --out PATH`. A timer rewrites the file every
// minute and another program (Vigil) reads it, so the rename is the only
// mutation of PATH.
package outfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrDirSync is returned (wrapped) when the rename succeeded and only the final
// directory fsync failed. The new file is in place and complete; what is not
// confirmed is that the rename survives a crash. Callers treat it as a warning,
// not a failed write: errors.Is(err, ErrDirSync).
var ErrDirSync = errors.New("directory sync failed after the rename")

// Mode is the permission of the published file. The status document holds no
// secrets, addresses or node names beyond what its schema allows, and the
// reader is usually another user, so it is world-readable regardless of umask.
const Mode os.FileMode = 0o644

// Write publishes data at path:
//
//  1. create a uniquely named temp file in the same directory as path
//     (never TMPDIR: rename is only atomic within one filesystem),
//  2. write, fsync, close, set mode 0644,
//  3. rename over path,
//  4. fsync the directory so the rename itself survives a crash.
//
// On any error before the rename the temp file is removed and path is left
// exactly as it was. Once the rename has succeeded the new file is in place; if
// only the final directory fsync then fails, the error wraps ErrDirSync so the
// caller can tell "not written" from "written, durability unconfirmed".
// A path that is a symbolic link is replaced by the new file, not followed.
//
// Errors never contain path: callers print them.
func Write(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".scry-out-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", scrub(err))
	}
	tmp := f.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmp)
		}
	}()

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("write temp file: %w", scrub(err))
	}
	if err := f.Chmod(Mode); err != nil {
		_ = f.Close()
		return fmt.Errorf("set mode: %w", scrub(err))
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync temp file: %w", scrub(err))
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", scrub(err))
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename: %w", scrub(err))
	}
	committed = true

	if err := syncDir(dir); err != nil {
		return fmt.Errorf("sync directory: %w: %w", ErrDirSync, scrub(err))
	}
	return nil
}

// syncDir fsyncs a directory so a completed rename survives a crash. It is a
// variable so a test can make it fail.
var syncDir = func(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // dir is the caller-chosen output directory
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return err
	}
	return d.Close()
}

// scrub drops the path an *os.PathError or *os.LinkError carries, keeping the
// operation and the errno, so nothing above can print a caller path.
func scrub(err error) error {
	switch e := err.(type) { //nolint:errorlint // only the outermost os error carries the path
	case *os.PathError:
		return fmt.Errorf("%s: %w", e.Op, e.Err)
	case *os.LinkError:
		return fmt.Errorf("%s: %w", e.Op, e.Err)
	}
	return err
}
