package fsx

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, p string, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSizeCountsFilesDirsAndHardlinks(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a", "one.bin"), 100_000)
	write(t, filepath.Join(root, "a", "b", "two.bin"), 50_000)
	write(t, filepath.Join(root, "outside.bin"), 10_000)
	// hardlink inside the measured tree to a file outside of it
	tree := filepath.Join(root, "a")
	if err := os.Link(filepath.Join(root, "outside.bin"), filepath.Join(tree, "link.bin")); err != nil {
		t.Fatal(err)
	}
	// hardlink with both links inside the tree
	if err := os.Link(filepath.Join(tree, "one.bin"), filepath.Join(tree, "b", "one-again.bin")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc", filepath.Join(tree, "sym")); err != nil {
		t.Fatal(err)
	}

	st, err := Size(context.Background(), tree, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.Files != 5 { // one, two, link, one-again, sym
		t.Errorf("files = %d, want 5", st.Files)
	}
	if st.Dirs != 2 {
		t.Errorf("dirs = %d, want 2", st.Dirs)
	}
	if st.Reclaim >= st.Bytes {
		t.Errorf("reclaim %d should be < bytes %d (outside hardlink)", st.Reclaim, st.Bytes)
	}
	if st.Bytes < 150_000 {
		t.Errorf("bytes = %d, want >= 150000", st.Bytes)
	}
	// compare with du (allocated, hardlinks counted once)
	out, err := exec.Command("du", "-sk", tree).Output()
	if err == nil {
		kb, _ := strconv.ParseInt(strings.Fields(string(out))[0], 10, 64)
		if diff := st.Bytes - kb*1024; diff > 8192 || diff < -8192 {
			t.Errorf("bytes = %d, du = %d", st.Bytes, kb*1024)
		}
	}
	if time.Since(st.Newest) > time.Minute {
		t.Errorf("newest = %v", st.Newest)
	}
}

func TestSizeSkipAndMissing(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "keep", "x.bin"), 40_000)
	write(t, filepath.Join(root, "skip", "y.bin"), 400_000)
	st, err := Size(context.Background(), root, &Options{Skip: func(p, name string) bool { return name == "skip" }})
	if err != nil {
		t.Fatal(err)
	}
	if st.Bytes > 200_000 {
		t.Errorf("skip not honoured: %d", st.Bytes)
	}
	if _, err := Size(context.Background(), filepath.Join(root, "nope"), nil); !os.IsNotExist(err) {
		t.Errorf("missing path: err = %v", err)
	}
}

func TestParse(t *testing.T) {
	for in, want := range map[string]int64{"500MB": 500e6, "1.5G": 1.5e9, "2GiB": 2 << 30, "10": 10, "": 0} {
		got, err := ParseBytes(in)
		if err != nil || got != want {
			t.Errorf("ParseBytes(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for in, want := range map[string]time.Duration{"30d": 30 * 24 * time.Hour, "2w": 14 * 24 * time.Hour, "12h": 12 * time.Hour, "7": 7 * 24 * time.Hour} {
		got, err := ParseAge(in)
		if err != nil || got != want {
			t.Errorf("ParseAge(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if Bytes(1_234_567_890) != "1.23 GB" {
		t.Errorf("Bytes = %s", Bytes(1_234_567_890))
	}
}
