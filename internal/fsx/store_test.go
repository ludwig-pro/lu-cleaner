package fsx

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/fsevents"
)

// storeHome prepares an isolated persistent cache: a temporary cache
// directory and a temporary HOME (the replayed root), spelled as FSEvents
// reports it.
func storeHome(t *testing.T) string {
	t.Helper()
	if !fsevents.Supported() {
		t.Skip("FSEvents unavailable in this build")
	}
	initAppData() // with the real HOME, before it changes
	old := SizeCacheDir
	SizeCacheDir = t.TempDir()
	t.Cleanup(func() { SizeCacheDir = old })
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("LU_NO_CACHE", "")
	t.Setenv("XDG_CACHE_HOME", "")
	return home
}

// tree creates n files of size bytes below root/a/b.
func tree(t *testing.T, root string, n, size int) {
	t.Helper()
	for i := range n {
		write(t, filepath.Join(root, "a", "b", "f"+strconv.Itoa(i)), size)
	}
}

type runResult struct {
	stats  map[string]Stats
	hits   int64
	walked int64 // trees walked
}

// storeRun is one engine run: open the store, measure paths, save.
func storeRun(t *testing.T, paths ...string) runResult {
	t.Helper()
	s := OpenSizeStore(context.Background())
	if s == nil {
		t.Fatal("store disabled")
	}
	ctx := WithSizeStore(context.Background(), s)
	w0, _ := Walked()
	res := runResult{stats: map[string]Stats{}}
	for _, p := range paths {
		st, err := Size(ctx, p, nil)
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("Size(%s): %v", p, err)
		}
		res.stats[p] = st
	}
	<-s.ready // (Close stops a replay still running)
	s.Close()
	if s.state != replayOK {
		t.Fatalf("replay %s: %s", stateName(s.state), s.reason)
	}
	w1, _ := Walked()
	res.hits, res.walked = s.hits.Load(), w1-w0
	return res
}

func TestStoreAnswersUnchangedTrees(t *testing.T) {
	home := storeHome(t)
	root := filepath.Join(home, "proj", "node_modules")
	tree(t, root, 40, 5000)
	first := storeRun(t, root)
	if first.hits != 0 || first.walked != 1 {
		t.Fatalf("first run: hits %d walked %d", first.hits, first.walked)
	}
	if _, err := os.Stat(SizeCachePath()); err != nil {
		t.Fatalf("cache not saved: %v", err)
	}
	second := storeRun(t, root)
	if second.hits != 1 || second.walked != 0 {
		t.Fatalf("second run: hits %d walked %d, want a hit without walking", second.hits, second.walked)
	}
	if second.stats[root] != first.stats[root] {
		t.Errorf("cached stats %+v, walked %+v", second.stats[root], first.stats[root])
	}
	// Entries stay valid run after run (their event id moves forward).
	if third := storeRun(t, root); third.hits != 1 {
		t.Errorf("third run: hits %d", third.hits)
	}
}

func TestStoreDeepChangeInvalidates(t *testing.T) {
	home := storeHome(t)
	root := filepath.Join(home, "cache")
	tree(t, root, 40, 5000)
	before := storeRun(t, root).stats[root]
	// Grow an existing file deep inside: no directory mtime changes, only
	// FSEvents can tell.
	f, err := os.OpenFile(filepath.Join(root, "a", "b", "f3"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.Write(make([]byte, 300_000))
	f.Close()
	after := storeRun(t, root)
	if after.hits != 0 || after.walked != 1 {
		t.Fatalf("hits %d walked %d: a deep change must invalidate the entry", after.hits, after.walked)
	}
	if after.stats[root].Bytes <= before.Bytes {
		t.Errorf("size %d, want more than %d", after.stats[root].Bytes, before.Bytes)
	}
}

// After a clean, the executor's deletions show up as events: the next scan
// re-walks what contained them.
func TestStoreDeletionsInvalidate(t *testing.T) {
	home := storeHome(t)
	outer := filepath.Join(home, "Library", "Caches", "tool")
	tree(t, outer, 40, 5000)
	write(t, filepath.Join(outer, "deep", "er", "big.bin"), 400_000)
	gone := filepath.Join(home, "proj", "node_modules")
	tree(t, gone, 40, 5000)
	before := storeRun(t, outer, gone).stats
	if err := os.RemoveAll(filepath.Join(outer, "deep", "er")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	after := storeRun(t, outer, gone)
	if after.hits != 0 {
		t.Fatalf("hits %d after deletions", after.hits)
	}
	if after.stats[outer].Bytes >= before[outer].Bytes {
		t.Errorf("size %d after deleting inside, want less than %d", after.stats[outer].Bytes, before[outer].Bytes)
	}
	if after.stats[gone].Bytes != 0 {
		t.Errorf("deleted tree measured %d bytes", after.stats[gone].Bytes)
	}
}

func TestStoreRootReplaced(t *testing.T) {
	home := storeHome(t)
	root := filepath.Join(home, "wt", "node_modules")
	tree(t, root, 40, 5000)
	storeRun(t, root)
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	tree(t, root, 40, 9000)
	if r := storeRun(t, root); r.hits != 0 || r.walked != 1 {
		t.Errorf("recreated root: hits %d walked %d", r.hits, r.walked)
	}
}

// An ancestor moved away, changed there (events under another path) and
// moved back: the rename events on the ancestor invalidate the entry.
func TestStoreAncestorMovedAwayAndBack(t *testing.T) {
	home := storeHome(t)
	proj := filepath.Join(home, "code", "proj")
	root := filepath.Join(proj, "node_modules")
	tree(t, root, 40, 5000)
	storeRun(t, root)
	away := filepath.Join(home, "elsewhere")
	if err := os.Rename(proj, away); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(away, "node_modules", "a", "b", "new.bin"), 200_000)
	if err := os.Rename(away, proj); err != nil {
		t.Fatal(err)
	}
	if r := storeRun(t, root); r.hits != 0 {
		t.Errorf("hits %d: the tree changed while its parent was elsewhere", r.hits)
	}
}

// What deleting a tree sharing data frees depends on the other copies: a
// change in one shared tree invalidates the others.
func TestStoreSharedTreesInvalidateEachOther(t *testing.T) {
	home := storeHome(t)
	store := filepath.Join(home, "Library", "pnpm", "store")
	nm := filepath.Join(home, "proj", "node_modules")
	plain := filepath.Join(home, "plain")
	tree(t, store, 40, 20_000)
	tree(t, plain, 40, 5000)
	for i := range 40 {
		name := filepath.Join("a", "b", "f"+strconv.Itoa(i))
		os.MkdirAll(filepath.Dir(filepath.Join(nm, name)), 0o755)
		if err := os.Link(filepath.Join(store, name), filepath.Join(nm, name)); err != nil {
			t.Fatal(err)
		}
	}
	first := storeRun(t, store, nm, plain)
	if st := first.stats[store]; st.Reclaim >= st.Bytes {
		t.Fatalf("store should share its files: %+v", st)
	}
	if r := storeRun(t, store, nm, plain); r.hits != 3 {
		t.Fatalf("hits %d, want 3", r.hits)
	}
	// The project's node_modules is deleted (lu-cleaner clean): the store
	// now frees everything, although nothing changed inside it.
	if err := os.RemoveAll(nm); err != nil {
		t.Fatal(err)
	}
	after := storeRun(t, store, plain)
	if after.hits != 1 {
		t.Errorf("hits %d, want 1 (the plain tree only)", after.hits)
	}
	if st := after.stats[store]; st.Reclaim != st.Bytes {
		t.Errorf("store after the project was deleted: reclaim %d, want %d", st.Reclaim, st.Bytes)
	}
}

func TestStoreSkipsIncompleteAndOptionWalks(t *testing.T) {
	home := storeHome(t)
	root := filepath.Join(home, "r")
	tree(t, root, 40, 1000)
	locked := filepath.Join(home, "locked")
	tree(t, locked, 40, 1000)
	write(t, filepath.Join(locked, "secret", "x"), 10)
	if err := os.Chmod(filepath.Join(locked, "secret"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(locked, "secret"), 0o755) })

	s := OpenSizeStore(context.Background())
	ctx, cancel := context.WithCancel(WithSizeStore(context.Background(), s))
	cancel()
	Size(ctx, root, nil) // cancelled: partial
	ctx = WithSizeStore(context.Background(), s)
	if st, _ := Size(ctx, locked, nil); st.Errors == 0 {
		t.Fatal("expected an unreadable entry")
	}
	Size(ctx, root, &Options{Skip: func(string, string) bool { return false }})
	Size(ctx, root, &Options{CrossDevice: true})
	s.Close()
	if n := len(s.fresh); n != 0 {
		t.Errorf("%d entries stored, want none (cancelled, unreadable, options)", n)
	}
}

func TestStoreSymlinkedSpelling(t *testing.T) {
	home := storeHome(t)
	real := filepath.Join(home, "real")
	tree(t, real, 40, 1000)
	link := filepath.Join(home, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(link, "a") // measured through the symlink
	storeRun(t, root)
	write(t, filepath.Join(real, "a", "b", "f7"), 300_000) // changed through the real path
	if r := storeRun(t, root); r.hits != 0 {
		t.Errorf("hits %d: the change was made through the resolved path", r.hits)
	}
}

func TestStoreDisabled(t *testing.T) {
	storeHome(t)
	t.Setenv("LU_NO_CACHE", "1")
	if OpenSizeStore(context.Background()) != nil {
		t.Error("LU_NO_CACHE=1 must disable the store")
	}
	t.Setenv("LU_NO_CACHE", "")
	SizeCacheDir = ""
	if OpenSizeStore(context.Background()) != nil {
		t.Error("tests must not use the real cache directory")
	}
	var s *SizeStore // nil store: plain per-run memo
	ctx := WithSizeStore(context.Background(), s)
	if _, err := Size(ctx, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	s.Close()
}

func TestStoreRejectsForeignOrOldFiles(t *testing.T) {
	home := storeHome(t)
	root := filepath.Join(home, "r")
	tree(t, root, 40, 1000)
	storeRun(t, root)
	file := SizeCachePath()
	load := func() int {
		s := OpenSizeStore(context.Background())
		defer s.Close()
		return len(s.entries)
	}
	if n := load(); n != 1 {
		t.Fatalf("%d entries loaded, want 1", n)
	}
	rewrite := func(edit func(*storeFile)) {
		s := OpenSizeStore(context.Background())
		<-s.ready
		f := storeFile{Version: storeVersion, Volume: s.volume, Boot: s.boot}
		for _, e := range s.entries {
			f.Entries = append(f.Entries, *e)
		}
		edit(&f)
		if err := writeStore(file, f); err != nil {
			t.Fatal(err)
		}
	}
	rewrite(func(f *storeFile) { f.Entries[0].Measured = time.Now().Add(-8 * 24 * time.Hour).UnixNano() })
	if n := load(); n != 0 {
		t.Errorf("an entry older than 7 days was loaded")
	}
	storeRun(t, root)
	rewrite(func(f *storeFile) {
		f.Entries[0].Shared = true
		f.Entries[0].Measured = time.Now().Add(-25 * time.Hour).UnixNano()
	})
	if n := load(); n != 0 {
		t.Errorf("a shared entry older than a day was loaded")
	}
	storeRun(t, root)
	rewrite(func(f *storeFile) { f.Volume = "another-volume" })
	if n := load(); n != 0 {
		t.Errorf("entries of another FSEvents database were loaded")
	}
	storeRun(t, root)
	rewrite(func(f *storeFile) { f.Boot = "another-boot-session" })
	if n := load(); n != 0 {
		t.Errorf("entries measured before the last boot were loaded")
	}
	storeRun(t, root)
	if n := load(); n != 1 {
		t.Fatalf("%d entries loaded after a new run, want 1", n)
	}
	os.WriteFile(file, []byte("garbage"), 0o600)
	if n := load(); n != 0 {
		t.Errorf("a corrupted file was loaded")
	}
	storeRun(t, root)
	rewrite(func(f *storeFile) {
		// one entry from the future is enough: the ids went backwards
		future := f.Entries[0]
		future.Path += "-other"
		future.EventID = fsevents.CurrentEventID() + 1_000_000
		f.Entries = append(f.Entries, future)
	})
	s := OpenSizeStore(context.Background())
	<-s.ready
	if s.state != replayFailed {
		t.Errorf("event ids from the future: state %s", stateName(s.state))
	}
	s.Close()
}

func TestStoreIndexRules(t *testing.T) {
	mk := func(p string, shared bool) *storedSize {
		return &storedSize{Path: p, Real: FoldPath(p), Shared: shared}
	}
	nm := mk("/Users/u/Code/proj/node_modules", false)
	sib := mk("/Users/u/Code/proj/node_modules2", false)
	other := mk("/Users/u/Library/Caches/x", false)
	store := mk("/Users/u/Library/pnpm/store", true)
	shared := mk("/Users/u/Code/app/node_modules", true)
	cafe := mk("/Users/u/Caf\u00e9/node_modules", false) // NFC
	all := []*storedSize{nm, sib, other, store, shared, cafe}
	cases := []struct {
		name string
		ev   fsevents.Event
		want []*storedSize
	}{
		{"inside", fsevents.Event{Path: "/Users/u/Code/proj/node_modules/a/b.js", Flags: fsevents.FlagItemModified}, []*storedSize{nm}},
		{"the root itself", fsevents.Event{Path: "/Users/u/Code/proj/node_modules", Flags: fsevents.FlagItemRenamed}, []*storedSize{nm}},
		{"case and NFC", fsevents.Event{Path: "/users/U/code/PROJ/node_modules/x"}, []*storedSize{nm}},
		{"sibling with the same prefix", fsevents.Event{Path: "/Users/u/Code/proj/node_modules2/x"}, []*storedSize{sib}},
		{"a file next to the root", fsevents.Event{Path: "/Users/u/Code/proj/package.json", Flags: fsevents.FlagItemModified}, nil},
		{"the parent directory itself", fsevents.Event{Path: "/Users/u/Code/proj", Flags: fsevents.FlagItemRenamed | fsevents.FlagItemIsDir}, []*storedSize{nm, sib}},
		// (shared trees invalidate each other)
		{"an ancestor", fsevents.Event{Path: "/Users/u/Code", Flags: fsevents.FlagItemRenamed}, []*storedSize{nm, sib, shared, store}},
		{"must scan above", fsevents.Event{Path: "/Users/u/Library", Flags: fsevents.FlagMustScanSubDirs}, []*storedSize{other, store, shared}},
		{"NFD spelling", fsevents.Event{Path: "/Users/u/Cafe\u0301/node_modules/x"}, []*storedSize{cafe}},
		{"must scan inside", fsevents.Event{Path: "/Users/u/Library/Caches/x/sub", Flags: fsevents.FlagMustScanSubDirs}, []*storedSize{other}},
		{"unrelated", fsevents.Event{Path: "/Users/u/Desktop/a.png"}, nil},
		{"shared tree changed", fsevents.Event{Path: "/Users/u/Code/app/node_modules/x"}, []*storedSize{shared, store}},
	}
	for _, c := range cases {
		ix := newStoreIndex(all)
		ix.apply(c.ev)
		ix.finish()
		want := map[*storedSize]bool{}
		for _, w := range c.want {
			want[w] = true
		}
		for _, e := range all {
			if ix.invalid[e] != want[e] {
				t.Errorf("%s: %s invalid=%v, want %v", c.name, e.Path, ix.invalid[e], want[e])
			}
		}
	}
}
