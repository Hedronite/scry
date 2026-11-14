// Command mkarchive writes a deterministic .tar.gz: the same inputs and the
// same Go toolchain give the same bytes on every machine.
//
// It exists because tar(1) differs between macOS and Linux, and a release must
// be reproducible from either. Stdlib only.
//
//	mkarchive -out FILE -dir NAME -mtime UNIX [-exec NAME]... NAME=SRC...
//
// Every entry lands under the top directory NAME/. Entries are sorted by name,
// carry uid/gid 0 and empty user and group names, the fixed mtime, mode 0644 (or
// 0755 for names given with -exec), and the gzip header has no file name and no
// timestamp.
package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"
)

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "mkarchive: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("mkarchive", flag.ContinueOnError)
	var (
		out   = fs.String("out", "", "archive file to write")
		dir   = fs.String("dir", "", "top-level directory inside the archive")
		mtime = fs.Int64("mtime", -1, "modification time of every entry, Unix seconds")
		execs multi
	)
	fs.Var(&execs, "exec", "entry name that gets mode 0755 (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" || *dir == "" || *mtime < 0 || fs.NArg() == 0 {
		return errors.New("usage: mkarchive -out FILE -dir NAME -mtime UNIX [-exec NAME] NAME=SRC (one or more entries)")
	}
	if strings.ContainsAny(*dir, "/\\") || *dir == "." || *dir == ".." {
		return fmt.Errorf("-dir must be a single path element")
	}
	isExec := map[string]bool{}
	for _, e := range execs {
		isExec[e] = true
	}

	type entry struct{ name, src string }
	var entries []entry
	seen := map[string]bool{}
	for _, a := range fs.Args() {
		name, src, ok := strings.Cut(a, "=")
		if !ok || name == "" || src == "" || path.Base(name) != name || name == "." || name == ".." {
			return fmt.Errorf("bad entry %q: want NAME=SRC with a plain NAME", a)
		}
		if seen[name] {
			return fmt.Errorf("duplicate entry %q", name)
		}
		seen[name] = true
		entries = append(entries, entry{name, src})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })

	f, err := os.Create(*out) //nolint:gosec // output path chosen by the release script
	if err != nil {
		return err
	}
	gz, err := gzip.NewWriterLevel(f, gzip.BestCompression)
	if err != nil {
		_ = f.Close()
		return err
	}
	// gzip.Header zero value: no Name, no Comment, ModTime zero, OS unknown.
	tw := tar.NewWriter(gz)
	when := time.Unix(*mtime, 0).UTC()

	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeDir, Name: *dir + "/", Mode: 0o755, ModTime: when, Format: tar.FormatUSTAR,
	}); err != nil {
		_ = f.Close()
		return err
	}
	for _, e := range entries {
		if err := addFile(tw, *dir+"/"+e.name, e.src, when, isExec[e.name]); err != nil {
			_ = f.Close()
			return err
		}
	}
	if err := tw.Close(); err != nil {
		_ = f.Close()
		return err
	}
	if err := gz.Close(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func addFile(tw *tar.Writer, name, src string, when time.Time, exec bool) error {
	in, err := os.Open(src) //nolint:gosec // source path chosen by the release script
	if err != nil {
		return err
	}
	defer in.Close() //nolint:errcheck // read-only
	st, err := in.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", src)
	}
	mode := int64(0o644)
	if exec {
		mode = 0o755
	}
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg, Name: name, Size: st.Size(), Mode: mode,
		Uid: 0, Gid: 0, Uname: "", Gname: "", ModTime: when, Format: tar.FormatUSTAR,
	}); err != nil {
		return err
	}
	_, err = io.Copy(tw, in)
	return err
}
