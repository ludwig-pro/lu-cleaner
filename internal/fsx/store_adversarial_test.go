package fsx

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/fsevents"
	"golang.org/x/sys/unix"
)

// Adversarial tests of the persistent size cache: every way found to make
// it answer with a size that is no longer true.

// grow appends n bytes to an existing file (no directory changes).
func grow(t *testing.T, p string, n int) {
	t.Helper()
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(make([]byte, n)); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

// fakeReplay replaces the FSEvents replay for the test.
func fakeReplay(t *testing.T, fn func(ctx context.Context, roots []string, since uint64, timeout time.Duration, cb func(fsevents.Event) bool) bool) {
	t.Helper()
	old := replayChanges
	replayChanges = fn
	t.Cleanup(func() { replayChanges = old })
}

// events replays exactly evs (whatever happened on disk).
func events(evs ...fsevents.Event) func(context.Context, []string, uint64, time.Duration, func(fsevents.Event) bool) bool {
	return func(_ context.Context, _ []string, _ uint64, _ time.Duration, cb func(fsevents.Event) bool) bool {
		for _, e := range evs {
			if !cb(e) {
				break
			}
		}
		return true
	}
}

// openRun opens the store and waits for its replay.
func openRun(t *testing.T) *SizeStore {
	t.Helper()
	s := OpenSizeStore(context.Background())
	if s == nil {
		t.Fatal("store disabled")
	}
	<-s.ready
	return s
}

// A file held open for writing (a VM disk, a database, a log) grows without
// any FSEvents event until it is closed: the cached size must not be used
// while such a file is open inside the tree.
func TestStoreFileWrittenWhileOpen(t *testing.T) {
	home := storeHome(t)
	root := filepath.Join(home, "vm")
	tree(t, root, 40, 1000)
	f, err := os.OpenFile(filepath.Join(root, "disk.img"), os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	f.Write(make([]byte, 100_000))
	f.Sync()
	first := storeRun(t, root)
	f.Write(make([]byte, 3_000_000))
	f.Sync()
	second := storeRun(t, root)
	if second.hits != 0 || second.stats[root].Bytes < first.stats[root].Bytes+2_900_000 {
		t.Errorf("hits %d: size %d, want about %d more (the open file grew)", second.hits, second.stats[root].Bytes, 3_000_000)
	}

	// The same with a file opened for writing after the tree was cached
	// (opening an existing file reports nothing either).
	other := filepath.Join(home, "db")
	tree(t, other, 40, 1000)
	storeRun(t, other)
	g, err := os.OpenFile(filepath.Join(other, "a", "b", "f1"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	g.Write(make([]byte, 2_000_000))
	if r := storeRun(t, other); r.hits != 0 {
		t.Errorf("hits %d: a file of the tree is open for writing", r.hits)
	}
	// Once closed, the close is an event: still not trusted.
	g.Close()
	if r := storeRun(t, other); r.hits != 0 {
		t.Errorf("hits %d after the writer closed its file", r.hits)
	}
	if r := storeRun(t, other); r.hits != 1 {
		t.Errorf("hits %d: nothing changed since the last walk", r.hits)
	}
}

func TestOpenForWriting(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := filepath.Join(dir, "written")
	r := filepath.Join(dir, "read")
	os.WriteFile(r, []byte("x"), 0o644)
	fw, err := os.OpenFile(w, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer fw.Close()
	fr, err := os.Open(r)
	if err != nil {
		t.Fatal(err)
	}
	defer fr.Close()
	paths, ok := openForWriting()
	if !ok {
		t.Fatal("process table unreadable")
	}
	if !slices.Contains(paths, w) {
		t.Errorf("%s (open read-write by this process) not listed", w)
	}
	if slices.Contains(paths, r) {
		t.Errorf("%s (open read-only) listed", r)
	}
	start := time.Now()
	openForWriting()
	if d := time.Since(start); d > time.Second {
		t.Errorf("listing the files open for writing took %v", d)
	}
}

func TestStoreIndexWriters(t *testing.T) {
	nm := &storedSize{Path: "/Users/u/proj/node_modules", Real: "/users/u/proj/node_modules"}
	store := &storedSize{Path: "/Users/u/Library/pnpm/store", Real: "/users/u/library/pnpm/store", Shared: true}
	shared := &storedSize{Path: "/Users/u/app/node_modules", Real: "/users/u/app/node_modules", Shared: true}
	logs := &storedSize{Path: "/Users/u/Library/Logs/x", Real: "/users/u/library/logs/x"}
	ix := newStoreIndex([]*storedSize{nm, store, shared, logs})
	ix.applyWriter("/Users/u/Library/Logs/x/today.log")
	ix.applyWriter("/Users/u/Library/Logs/xy.log") // a sibling, not inside
	ix.applyWriter("/Users/u/app/node_modules/.cache/db")
	ix.finish()
	for e, want := range map[*storedSize]bool{nm: false, store: true, shared: true, logs: true} {
		if ix.invalid[e] != want {
			t.Errorf("%s invalid=%v, want %v", e.Path, ix.invalid[e], want)
		}
	}
	if ix.byWriters != 2 || ix.byContent != 0 || ix.byShared != 1 {
		t.Errorf("counts: writers %d content %d shared %d", ix.byWriters, ix.byContent, ix.byShared)
	}
}

// Nothing is trusted when the files open for writing cannot be listed.
func TestStoreWritersUnavailable(t *testing.T) {
	home := storeHome(t)
	root := filepath.Join(home, "r")
	tree(t, root, 40, 1000)
	storeRun(t, root)
	old := listWriters
	listWriters = func() ([]string, bool) { return nil, false }
	t.Cleanup(func() { listWriters = old })
	s := openRun(t)
	defer s.Close()
	if s.state != replayFailed {
		t.Errorf("state %s, want failed", stateName(s.state))
	}
	if _, ok := s.lookup(context.Background(), root); ok {
		t.Error("hit without the list of files open for writing")
	}
}

// HOME on another volume: its history misses what changed while it was
// mounted elsewhere, or here with its ownership ignored.
func TestOnBootDataVolume(t *testing.T) {
	if !onBootDataVolume(t.TempDir()) {
		t.Error("the temporary directory is on the data volume")
	}
	for _, p := range []string{"/dev", "/nonexistent"} {
		if onBootDataVolume(p) {
			t.Errorf("%s: not on the data volume", p)
		}
	}
	if bootSession() == "" {
		t.Error("no boot session id")
	}
}

// The real thing, opt-in (it mounts a small disk image): a volume mounted
// with its ownership ignored does not record FSEvents history, so changes
// made then were invisible to the next scan once it was mounted normally.
func TestStoreHomeOnAnotherVolume(t *testing.T) {
	if os.Getenv("LU_TEST_HDIUTIL") == "" {
		t.Skip("set LU_TEST_HDIUTIL=1 to mount a disk image")
	}
	home := storeHome(t) // cache directory; HOME is replaced below
	img := filepath.Join(home, "vol.sparseimage")
	mnt := filepath.Join(home, "mnt")
	os.Mkdir(mnt, 0o755)
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("hdiutil", args...).CombinedOutput(); err != nil {
			t.Fatalf("hdiutil %v: %v\n%s", args, err, out)
		}
	}
	run("create", "-quiet", "-size", "40m", "-fs", "APFS", "-type", "SPARSE", "-volname", "LUTEST", img)
	run("attach", "-quiet", "-owners", "on", "-mountpoint", mnt, img)
	t.Cleanup(func() { exec.Command("hdiutil", "detach", "-force", mnt).Run() })
	vhome := filepath.Join(mnt, "home")
	root := filepath.Join(vhome, "proj", "node_modules")
	tree(t, root, 40, 5000)
	t.Setenv("HOME", vhome)
	for i := 0; fsevents.VolumeUUID(vhome) == ""; i++ {
		if i == 50 {
			t.Skip("no FSEvents history on the disk image")
		}
		time.Sleep(100 * time.Millisecond) // fseventsd sets it up after the mount
	}
	if s := OpenSizeStore(context.Background()); s != nil {
		s.Close()
		t.Fatal("the size cache must be disabled for a HOME on another volume")
	}
}

// Changes made during a run, after the walk (or after the replay validated
// an entry), are replayed by the next run: entries are saved with the event
// id taken before any walk, not a later one.
func TestStoreChangesDuringTheRun(t *testing.T) {
	home := storeHome(t)
	a := filepath.Join(home, "a")
	b := filepath.Join(home, "b")
	tree(t, a, 40, 1000)
	tree(t, b, 40, 1000)
	storeRun(t, b) // cached before this run

	s := openRun(t)
	ctx := WithSizeStore(context.Background(), s)
	Size(ctx, a, nil)                                  // walked, recorded
	grow(t, filepath.Join(a, "a", "b", "f1"), 500_000) // changed after its walk
	grow(t, filepath.Join(b, "a", "b", "f1"), 500_000) // changed after the replay validated it
	if _, err := Size(ctx, b, nil); err != nil {
		t.Fatal(err)
	}
	s.Close()

	r := storeRun(t, a, b)
	if r.hits != 0 {
		t.Errorf("hits %d: both trees changed after the event id of their entry", r.hits)
	}
}

// Two runs at once (two lu-cleaner processes): the last one to save may
// write an older measure, still stamped with its own (older) event id.
func TestStoreConcurrentRuns(t *testing.T) {
	home := storeHome(t)
	root := filepath.Join(home, "r")
	tree(t, root, 40, 1000)
	A := openRun(t)
	B := openRun(t)
	before, _ := Size(WithSizeStore(context.Background(), A), root, nil)
	grow(t, filepath.Join(root, "a", "b", "f2"), 800_000)
	after, _ := Size(WithSizeStore(context.Background(), B), root, nil)
	B.Close()
	A.Close() // overwrites B's newer entry with the older measure
	if after.Bytes <= before.Bytes {
		t.Fatalf("B measured %d, A %d", after.Bytes, before.Bytes)
	}
	r := storeRun(t, root)
	if r.hits != 0 || r.stats[root] != after {
		t.Errorf("hits %d, size %d: want a walk measuring %d", r.hits, r.stats[root].Bytes, after.Bytes)
	}
	// The file is whole (atomic rename), whatever the order.
	if f, ok := decodeStore(mustRead(t, SizeCachePath())); !ok || len(f.Entries) != 1 {
		t.Errorf("cache file after concurrent saves: ok=%v %d entries", ok, len(f.Entries))
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Data shared with other files changes without events inside the tree in
// some cases (see store.go); these ones do report an event inside it.
func TestStoreLinksAndClonesMadeOutside(t *testing.T) {
	home := storeHome(t)
	root := filepath.Join(home, "cache")
	tree(t, root, 40, 20_000)
	outside := filepath.Join(home, "elsewhere")
	os.MkdirAll(outside, 0o755)
	first := storeRun(t, root).stats[root]
	if first.Reclaim != first.Bytes {
		t.Fatalf("nothing shared yet: %+v", first)
	}
	// A hard link made from outside: deleting the tree frees less.
	if err := os.Link(filepath.Join(root, "a", "b", "f1"), filepath.Join(outside, "link")); err != nil {
		t.Fatal(err)
	}
	r := storeRun(t, root)
	if r.hits != 0 || r.stats[root].Reclaim >= first.Reclaim {
		t.Errorf("after a hard link from outside: hits %d, reclaim %d (was %d)", r.hits, r.stats[root].Reclaim, first.Reclaim)
	}
	// A clone made outside.
	if err := unix.Clonefile(filepath.Join(root, "a", "b", "f2"), filepath.Join(outside, "clone"), 0); err != nil {
		t.Skipf("clonefile: %v", err)
	}
	if r := storeRun(t, root); r.hits != 0 {
		t.Errorf("hits %d after a clone was made outside", r.hits)
	}
	// Written through the outside link: both paths are reported.
	grow(t, filepath.Join(outside, "link"), 300_000)
	if r := storeRun(t, root); r.hits != 0 {
		t.Errorf("hits %d after a write through a hard link outside", r.hits)
	}
}

// A directory of the tree swapped with one outside (renamex_np
// RENAME_SWAP, atomic save of a whole folder).
func TestStoreSubdirSwappedWithOutside(t *testing.T) {
	home := storeHome(t)
	root := filepath.Join(home, "r")
	tree(t, root, 40, 1000)
	other := filepath.Join(home, "other", "b")
	write(t, filepath.Join(other, "big"), 2_000_000)
	storeRun(t, root)
	if err := unix.RenamexNp(filepath.Join(root, "a", "b"), other, unix.RENAME_SWAP); err != nil {
		t.Skipf("RENAME_SWAP: %v", err)
	}
	if r := storeRun(t, root); r.hits != 0 {
		t.Errorf("hits %d after a folder of the tree was swapped", r.hits)
	}
}

// A symlink along the requested path now points elsewhere.
func TestStoreSymlinkRetargeted(t *testing.T) {
	home := storeHome(t)
	one := filepath.Join(home, "one")
	two := filepath.Join(home, "two")
	tree(t, one, 40, 1000)
	tree(t, two, 40, 9000)
	link := filepath.Join(home, "current")
	if err := os.Symlink(one, link); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(link, "a")
	storeRun(t, root)
	// ln -sfn two current: an event on the link only, outside both trees.
	tmp := link + ".new"
	os.Symlink(two, tmp)
	if err := os.Rename(tmp, link); err != nil {
		t.Fatal(err)
	}
	r := storeRun(t, root)
	want, _ := Size(context.Background(), filepath.Join(two, "a"), nil)
	if r.hits != 0 || r.stats[root].Bytes != want.Bytes {
		t.Errorf("hits %d, size %d: want %d (the link points to another tree)", r.hits, r.stats[root].Bytes, want.Bytes)
	}
}

// Paths requested with another case, changes made through the on-disk
// spelling, and an ancestor renamed to another case.
func TestStoreCaseVariants(t *testing.T) {
	home := storeHome(t)
	root := filepath.Join(home, "Code", "proj", "node_modules")
	tree(t, root, 40, 1000)
	asked := filepath.Join(home, "code", "PROJ", "node_modules")
	if _, err := os.Stat(asked); err != nil {
		t.Skip("case-sensitive volume")
	}
	storeRun(t, asked)
	grow(t, filepath.Join(root, "a", "b", "f1"), 300_000)
	if r := storeRun(t, asked); r.hits != 0 {
		t.Errorf("hits %d: changed through the on-disk spelling", r.hits)
	}
	if r := storeRun(t, asked); r.hits != 1 {
		t.Fatalf("hits %d, want 1", r.hits)
	}
	// Code -> code (case only), changed deep inside meanwhile.
	tmp := filepath.Join(home, "tmpname")
	os.Rename(filepath.Join(home, "Code"), tmp)
	grow(t, filepath.Join(tmp, "proj", "node_modules", "a", "b", "f2"), 300_000)
	os.Rename(tmp, filepath.Join(home, "code"))
	if r := storeRun(t, asked); r.hits != 0 {
		t.Errorf("hits %d after the ancestor was renamed and changed", r.hits)
	}
}

// When the history misses events (simulated), the identity of the root
// still catches a root deleted and created again, or replaced by a rename.
func TestStoreRootIdentityWithoutEvents(t *testing.T) {
	home := storeHome(t)
	root := filepath.Join(home, "r")
	tree(t, root, 40, 1000)
	storeRun(t, root)
	fakeReplay(t, events()) // no event at all
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	tree(t, root, 40, 1000)
	if r := storeRun(t, root); r.hits != 0 {
		t.Errorf("hits %d: root deleted and created again", r.hits)
	}
	other := filepath.Join(home, "other")
	tree(t, other, 40, 7000)
	os.RemoveAll(root)
	if err := os.Rename(other, root); err != nil {
		t.Fatal(err)
	}
	if r := storeRun(t, root); r.hits != 0 {
		t.Errorf("hits %d: root replaced by a rename", r.hits)
	}
}

// What the history says when it cannot be trusted.
func TestStoreUntrustedHistory(t *testing.T) {
	home := storeHome(t)
	root := filepath.Join(home, "r")
	tree(t, root, 40, 1000)
	for _, c := range []struct {
		name   string
		replay func(context.Context, []string, uint64, time.Duration, func(fsevents.Event) bool) bool
	}{
		{"ids wrapped", events(fsevents.Event{Path: "/", Flags: fsevents.FlagEventIdsWrapped})},
		{"kernel dropped", events(fsevents.Event{Path: "/", Flags: fsevents.FlagMustScanSubDirs | fsevents.FlagKernelDropped})},
		{"user dropped", events(fsevents.Event{Path: home, Flags: fsevents.FlagMustScanSubDirs | fsevents.FlagUserDropped})},
		{"root changed", events(fsevents.Event{Path: home, Flags: fsevents.FlagRootChanged})},
		{"unavailable", func(context.Context, []string, uint64, time.Duration, func(fsevents.Event) bool) bool { return false }},
	} {
		t.Run(c.name, func(t *testing.T) {
			storeRun(t, root) // (real replay) cached and valid
			fakeReplay(t, c.replay)
			s := openRun(t)
			if s.state != replayFailed {
				t.Errorf("state %s, want failed", stateName(s.state))
			}
			if _, ok := s.lookup(context.Background(), root); ok {
				t.Error("hit from an untrusted history")
			}
			s.Close()
			if f, ok := decodeStore(mustRead(t, SizeCachePath())); !ok || len(f.Entries) != 0 {
				t.Errorf("%d entries kept after an untrusted replay", len(f.Entries))
			}
		})
	}
	storeRun(t, root)
	fakeReplay(t, events(fsevents.Event{Path: "/", Flags: fsevents.FlagMustScanSubDirs}))
	if r := storeRun(t, root); r.hits != 0 {
		t.Errorf("hits %d after a rescan of /", r.hits)
	}
}

// A run that ends before its replay (a quick command) stops it, and keeps
// the entries as they were: the next run still replays their history.
func TestStoreCloseStopsTheReplay(t *testing.T) {
	home := storeHome(t)
	root := filepath.Join(home, "r")
	tree(t, root, 40, 1000)
	storeRun(t, root)
	grow(t, filepath.Join(root, "a", "b", "f1"), 400_000)

	real := replayChanges
	fakeReplay(t, func(ctx context.Context, _ []string, _ uint64, _ time.Duration, _ func(fsevents.Event) bool) bool {
		<-ctx.Done() // a long replay
		return false
	})
	s := OpenSizeStore(context.Background())
	start := time.Now()
	s.Close()
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("Close waited %v for the replay", d)
	}
	if s.state != replayCancelled {
		t.Errorf("state %s, want cancelled", stateName(s.state))
	}
	replayChanges = real
	if r := storeRun(t, root); r.hits != 0 {
		t.Errorf("hits %d: the change happened before the cancelled run", r.hits)
	}
}

// Every damaged byte makes the whole file ignored (a flipped bit in an event
// id would silently skip changes).
func TestStoreCorruptedFile(t *testing.T) {
	home := storeHome(t)
	root := filepath.Join(home, "r")
	tree(t, root, 40, 1000)
	storeRun(t, root)
	file := SizeCachePath()
	good := mustRead(t, file)
	orig, ok := decodeStore(good)
	if !ok || len(orig.Entries) != 1 {
		t.Fatalf("saved file: ok=%v", ok)
	}
	for i := range good {
		for _, bit := range []byte{0x01, 0x80} {
			bad := slices.Clone(good)
			bad[i] ^= bit
			if f, ok := decodeStore(bad); ok {
				t.Fatalf("byte %d flipped (%#x): decoded %+v", i, bit, f.Entries)
			}
		}
	}
	for _, n := range []int{0, 3, len(storeMagic) + 4, len(good) / 2, len(good) - 1} {
		if _, ok := decodeStore(good[:n]); ok {
			t.Errorf("truncated to %d bytes: decoded", n)
		}
	}
	// Loading a damaged file starts from scratch.
	bad := slices.Clone(good)
	bad[len(bad)-3] ^= 0x10
	os.WriteFile(file, bad, 0o600)
	s := openRun(t)
	n := len(s.entries)
	s.Close()
	if n != 0 {
		t.Errorf("%d entries loaded from a damaged file", n)
	}
	// A cache path that is a directory, or unreadable: no cache, no failure.
	os.Remove(file)
	os.Mkdir(file, 0o700)
	if r := storeRun(t, root); r.hits != 0 {
		t.Errorf("hits %d with a directory as the cache file", r.hits)
	}
	os.Remove(file)
}

// Without FSEvents (no cgo) there is no persistent cache, and sizes still
// work.
func TestStoreWithoutFSEvents(t *testing.T) {
	if fsevents.Supported() {
		t.Skip("FSEvents available: see the other tests")
	}
	old := SizeCacheDir
	SizeCacheDir = t.TempDir()
	t.Cleanup(func() { SizeCacheDir = old })
	if s := OpenSizeStore(context.Background()); s != nil {
		t.Fatal("a store without FSEvents")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "f"), make([]byte, 5000), 0o644)
	st, err := Size(WithSizeStore(context.Background(), nil), dir, nil)
	if err != nil || st.Files != 1 {
		t.Errorf("Size: %+v %v", st, err)
	}
	if entries, _ := os.ReadDir(SizeCacheDir); len(entries) != 0 {
		t.Errorf("cache written without FSEvents: %v", entries)
	}
}

// Long paths: FSEvents reports a change in a file whose path does not fit
// in 1024 bytes as a rescan of its directory.
func TestStoreLongPath(t *testing.T) {
	home := storeHome(t)
	root := filepath.Join(home, "deep")
	tree(t, root, 40, 1000)
	p := root
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
	name := strings.Repeat("f", 200) // p/name is longer than PATH_MAX
	f, err := unix.Openat(fd, name, unix.O_CREAT|unix.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	unix.Write(f, make([]byte, 1000))
	unix.Close(f)
	storeRun(t, root)
	f, err = unix.Openat(fd, name, unix.O_WRONLY|unix.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	unix.Write(f, make([]byte, 500_000))
	unix.Close(f)
	if r := storeRun(t, root); r.hits != 0 {
		t.Errorf("hits %d after a change in a file with a path over 1024 bytes", r.hits)
	}
}

// An entry too old to replay is dropped alone: the others are still
// validated (it used to fail the whole replay).
func TestStoreOldEntryDroppedAlone(t *testing.T) {
	home := storeHome(t)
	oldRoot := filepath.Join(home, "old")
	root := filepath.Join(home, "r")
	tree(t, oldRoot, 40, 1000)
	tree(t, root, 40, 1000)
	storeRun(t, oldRoot, root)
	s := openRun(t)
	f := storeFile{Version: storeVersion, Volume: s.volume, Boot: s.boot}
	for _, e := range s.entries {
		c := *e
		if c.Path == oldRoot {
			c.EventID = s.runID - replayMaxWindow - 1000
		}
		f.Entries = append(f.Entries, c)
	}
	s.Close()
	if err := writeStore(SizeCachePath(), f); err != nil {
		t.Fatal(err)
	}
	r := storeRun(t, oldRoot, root)
	if r.hits != 1 || r.walked != 1 {
		t.Errorf("hits %d walked %d: want the recent entry answered, the old one walked", r.hits, r.walked)
	}
}

// Temporary files left by a run killed while saving are removed later.
func TestStoreRemovesStaleTempFiles(t *testing.T) {
	storeHome(t)
	dir := filepath.Dir(SizeCachePath())
	os.MkdirAll(dir, 0o700)
	stale := filepath.Join(dir, ".sizes-111")
	recent := filepath.Join(dir, ".sizes-222")
	os.WriteFile(stale, []byte("x"), 0o600)
	os.WriteFile(recent, []byte("x"), 0o600)
	past := time.Now().Add(-2 * time.Hour)
	os.Chtimes(stale, past, past)
	if err := writeStore(SizeCachePath(), storeFile{Version: storeVersion}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("stale temporary file kept")
	}
	if _, err := os.Stat(recent); err != nil {
		t.Error("a recent temporary file (another run saving now) was removed")
	}
}

// APFS folds names more than lower-casing does (ß and SS, final and medial
// sigma, ligatures): a root requested with such a spelling must still be
// matched with the events, which carry the on-disk name.
func TestStoreFullCaseFoldingSpelling(t *testing.T) {
	home := storeHome(t)
	for _, c := range []struct{ disk, asked string }{
		{"straße", "STRASSE"},
		{"λογος", "λογοσ"}, // λογος / λογοσ
		{"ﬁles", "files"},
	} {
		root := filepath.Join(home, c.disk)
		tree(t, root, 40, 1000)
		asked := filepath.Join(home, c.asked)
		if _, err := os.Stat(asked); err != nil {
			t.Skipf("%s does not resolve to %s here (case-sensitive volume?)", c.asked, c.disk)
		}
		storeRun(t, asked)
		grow(t, filepath.Join(root, "a", "b", "f1"), 400_000)
		if r := storeRun(t, asked); r.hits != 0 {
			t.Errorf("%s asked as %s: hits %d after a change inside", c.disk, c.asked, r.hits)
		}
	}
}
