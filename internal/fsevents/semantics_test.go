package fsevents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// What the size cache (fsx) relies on in the FSEvents history: a change
// made through a path outside a tree, but which changes the tree, still
// reports an event inside it.

// replayed returns the events recorded below dir since the id since, by
// path (flags OR-ed).
func replayed(t *testing.T, dir string, since uint64) map[string]uint32 {
	t.Helper()
	got := map[string]uint32{}
	if !Changes(context.Background(), []string{dir}, since, 5*time.Second, func(e Event) bool {
		got[e.Path] |= e.Flags
		return true
	}) {
		t.Fatal("replay failed")
	}
	return got
}

func syncID(t *testing.T) uint64 {
	t.Helper()
	id, ok := Sync(context.Background(), 5*time.Second)
	if !ok {
		t.Fatal("Sync failed")
	}
	return id
}

func TestHistoryReportsChangesMadeFromOutside(t *testing.T) {
	requireSupported(t)
	dir := realTempDir(t)
	tree := filepath.Join(dir, "R")
	src := filepath.Join(tree, "sub", "f")
	os.MkdirAll(filepath.Dir(src), 0o755)
	os.WriteFile(src, make([]byte, 300_000), 0o644)
	outside := filepath.Join(dir, "X")
	os.MkdirAll(outside, 0o755)

	// A hard link made from outside: the source's directory is reported.
	since := syncID(t)
	if err := os.Link(src, filepath.Join(outside, "link")); err != nil {
		t.Fatal(err)
	}
	if got := replayed(t, dir, since); got[filepath.Dir(src)] == 0 && got[src] == 0 {
		t.Errorf("hard link from outside: nothing reported inside the tree: %v", got)
	}

	// A write through that link: both paths.
	since = syncID(t)
	f, err := os.OpenFile(filepath.Join(outside, "link"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.Write(make([]byte, 100_000))
	f.Close()
	if got := replayed(t, dir, since); got[src]&FlagItemModified == 0 {
		t.Errorf("write through an outside hard link: %v", got)
	}

	// A clone made outside: the source is reported.
	g := filepath.Join(tree, "sub", "g")
	os.WriteFile(g, make([]byte, 300_000), 0o644)
	since = syncID(t)
	if err := unix.Clonefile(g, filepath.Join(outside, "clone"), 0); err != nil {
		t.Skipf("clonefile: %v", err)
	}
	if got := replayed(t, dir, since); got[g]&FlagItemCloned == 0 {
		t.Errorf("clone made outside: source not reported: %v", got)
	}

	// A folder swapped with one outside: both paths.
	inner := filepath.Join(tree, "sub")
	other := filepath.Join(outside, "swap")
	os.MkdirAll(other, 0o755)
	since = syncID(t)
	if err := unix.RenamexNp(inner, other, unix.RENAME_SWAP); err != nil {
		t.Skipf("RENAME_SWAP: %v", err)
	}
	if got := replayed(t, dir, since); got[inner] == 0 || got[other] == 0 {
		t.Errorf("RENAME_SWAP: %v", got)
	}
}

// A path too long for an event is reported as a rescan of its directory.
func TestHistoryLongPath(t *testing.T) {
	requireSupported(t)
	dir := realTempDir(t)
	p := dir
	for len(p) < 960 {
		p = filepath.Join(p, strings.Repeat("d", 40))
	}
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	fd, err := unix.Open(p, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	since := syncID(t)
	f, err := unix.Openat(fd, strings.Repeat("f", 200), unix.O_CREAT|unix.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	unix.Write(f, make([]byte, 1000))
	unix.Close(f)
	if got := replayed(t, dir, since); got[p]&FlagMustScanSubDirs == 0 {
		t.Errorf("long path: want a rescan of %s, got %v", p, got)
	}
}

// Several changes of one path are coalesced under the id of the last one:
// a replay since an id taken between them still reports the path.
func TestHistoryCoalescedEventsKeepTheLastID(t *testing.T) {
	requireSupported(t)
	dir := realTempDir(t)
	f := filepath.Join(dir, "f")
	os.WriteFile(f, []byte("a"), 0o644)
	since := syncID(t)
	g, _ := os.OpenFile(f, os.O_WRONLY|os.O_APPEND, 0)
	g.Write([]byte("b"))
	g.Close()
	if got := replayed(t, dir, since); got[f] == 0 {
		t.Errorf("second change of a path not reported: %v", got)
	}
}

// Writes to a file that stays open are reported only when it is closed (or
// truncated): the reason fsx lists the files open for writing. Logged, not
// asserted: a system reporting them earlier would only be safer.
func TestHistoryOpenFileWrites(t *testing.T) {
	requireSupported(t)
	dir := realTempDir(t)
	p := filepath.Join(dir, "log")
	os.WriteFile(p, nil, 0o644)
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	since := syncID(t)
	f.Write(make([]byte, 1<<20))
	f.Sync()
	t.Logf("while open: %v", replayed(t, dir, since))
	f.Close()
	if got := replayed(t, dir, since); got[p]&FlagItemModified == 0 {
		t.Errorf("the close of a written file must be reported: %v", got)
	}
}
