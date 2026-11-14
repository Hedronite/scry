package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func inputs(t *testing.T) (dir string) {
	t.Helper()
	dir = t.TempDir()
	for name, body := range map[string]string{"bin": "binary", "lic": "licence", "rd": "readme"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func build(t *testing.T, dir, out string) []byte {
	t.Helper()
	args := []string{"-out", out, "-dir", "scry_0.1.0_linux_amd64", "-mtime", "1763100000", "-exec", "scry",
		"scry=" + filepath.Join(dir, "bin"), "README.md=" + filepath.Join(dir, "rd"), "LICENSE=" + filepath.Join(dir, "lic")}
	if err := run(args); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestArchiveIsByteIdenticalAcrossRunsAndTimes(t *testing.T) {
	dir := inputs(t)
	a := build(t, dir, filepath.Join(t.TempDir(), "a.tar.gz"))
	// Touch the inputs: a different file mtime must not change the bytes.
	later := time.Now().Add(48 * time.Hour)
	for _, n := range []string{"bin", "lic", "rd"} {
		if err := os.Chtimes(filepath.Join(dir, n), later, later); err != nil {
			t.Fatal(err)
		}
	}
	b := build(t, dir, filepath.Join(t.TempDir(), "b.tar.gz"))
	if !bytes.Equal(a, b) {
		t.Fatal("two builds differ")
	}
}

func TestArchiveHeadersAreNormalised(t *testing.T) {
	raw := build(t, inputs(t), filepath.Join(t.TempDir(), "a.tar.gz"))

	// gzip header: magic, method, flags 0 (no name, no comment, no extra),
	// mtime 0, OS 255 (unknown).
	if len(raw) < 10 || raw[0] != 0x1f || raw[1] != 0x8b || raw[3] != 0 ||
		!bytes.Equal(raw[4:8], []byte{0, 0, 0, 0}) || raw[9] != 255 {
		t.Fatalf("gzip header = % x", raw[:10])
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if zr.Name != "" || zr.Comment != "" || !zr.ModTime.IsZero() {
		t.Fatalf("gzip header carries %q %q %v", zr.Name, zr.Comment, zr.ModTime)
	}
	tr := tar.NewReader(zr)
	var names []string
	want := time.Unix(1763100000, 0).UTC()
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
		if h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" {
			t.Errorf("%s: owner %d:%d %q:%q", h.Name, h.Uid, h.Gid, h.Uname, h.Gname)
		}
		if !h.ModTime.Equal(want) || !h.AccessTime.IsZero() || !h.ChangeTime.IsZero() {
			t.Errorf("%s: times %v %v %v", h.Name, h.ModTime, h.AccessTime, h.ChangeTime)
		}
		switch h.Name {
		case "scry_0.1.0_linux_amd64/scry", "scry_0.1.0_linux_amd64/":
			if h.Mode != 0o755 {
				t.Errorf("%s mode %o", h.Name, h.Mode)
			}
		default:
			if h.Mode != 0o644 {
				t.Errorf("%s mode %o", h.Name, h.Mode)
			}
		}
	}
	wantNames := []string{
		"scry_0.1.0_linux_amd64/", "scry_0.1.0_linux_amd64/LICENSE",
		"scry_0.1.0_linux_amd64/README.md", "scry_0.1.0_linux_amd64/scry",
	}
	if len(names) != len(wantNames) {
		t.Fatalf("entries = %v", names)
	}
	for i := range names {
		if names[i] != wantNames[i] {
			t.Fatalf("entries = %v, want %v", names, wantNames)
		}
	}
}

func TestBadArguments(t *testing.T) {
	d := inputs(t)
	src := filepath.Join(d, "bin")
	for name, args := range map[string][]string{
		"no entries":   {"-out", filepath.Join(d, "o"), "-dir", "x", "-mtime", "1"},
		"no mtime":     {"-out", filepath.Join(d, "o"), "-dir", "x", "a=" + src},
		"nested dir":   {"-out", filepath.Join(d, "o"), "-dir", "x/y", "-mtime", "1", "a=" + src},
		"path in name": {"-out", filepath.Join(d, "o"), "-dir", "x", "-mtime", "1", "../a=" + src},
		"duplicate":    {"-out", filepath.Join(d, "o"), "-dir", "x", "-mtime", "1", "a=" + src, "a=" + src},
		"directory":    {"-out", filepath.Join(d, "o"), "-dir", "x", "-mtime", "1", "a=" + d},
	} {
		if err := run(args); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
