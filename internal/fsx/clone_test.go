package fsx

import (
	"context"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"golang.org/x/sys/unix"
)

const mb = 1 << 20

func writeRandom(t *testing.T, p string, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, n)
	rand.New(rand.NewSource(int64(n))).Read(b)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func clone(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := unix.Clonefile(src, dst, unix.CLONE_NOFOLLOW); err != nil {
		t.Skipf("clonefile unsupported here: %v", err)
	}
}

func measure(t *testing.T, p string) Stats {
	t.Helper()
	st, err := Size(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// near reports whether got is within 64 KiB of want (metadata blocks).
func near(got, want int64) bool { return got >= want-64<<10 && got <= want+64<<10 }

// APFS clones (bun / pnpm installs, cp -c) share their data: deleting one
// copy frees only its private bytes. They used to be counted as fully
// reclaimable (a 1.2 GB bun node_modules "freed" ~0 bytes).
func TestSizeClonesAreNotReclaimable(t *testing.T) {
	if !useBulk || !useCloneAttrs {
		t.Skip("bulk attributes disabled")
	}
	root := t.TempDir()
	store := filepath.Join(root, "store", "pkg.bin")
	writeRandom(t, store, 4*mb)
	nm := filepath.Join(root, "proj", "node_modules")
	clone(t, store, filepath.Join(nm, "pkg", "pkg.bin"))
	writeRandom(t, filepath.Join(nm, "own.bin"), 1*mb) // not shared

	st := measure(t, nm)
	if !near(st.Bytes, 5*mb) {
		t.Errorf("Bytes = %d, want ~5 MiB (allocated size, clones included)", st.Bytes)
	}
	if !near(st.Reclaim, 1*mb) {
		t.Errorf("Reclaim = %d, want ~1 MiB: the clone shares its data with the store", st.Reclaim)
	}

	// Both copies inside the measured tree: the shared data is freed, once.
	st = measure(t, root)
	if !near(st.Bytes, 9*mb) || !near(st.Reclaim, 5*mb) {
		t.Errorf("whole tree: Bytes = %d (want ~9 MiB), Reclaim = %d (want ~5 MiB)", st.Bytes, st.Reclaim)
	}

	// A single cloned file measured on its own.
	if st := measure(t, filepath.Join(nm, "pkg", "pkg.bin")); st.Reclaim > 64<<10 {
		t.Errorf("single clone: Reclaim = %d, want ~0", st.Reclaim)
	}
	// The store alone does not free the data either while the clone exists.
	if st := measure(t, filepath.Join(root, "store")); st.Reclaim > 64<<10 {
		t.Errorf("store: Reclaim = %d, want ~0", st.Reclaim)
	}
}

func TestSizeModifiedCloneAndHardlinks(t *testing.T) {
	if !useBulk || !useCloneAttrs {
		t.Skip("bulk attributes disabled")
	}
	root := t.TempDir()
	orig := filepath.Join(root, "orig.bin")
	writeRandom(t, orig, 4*mb)
	tree := filepath.Join(root, "tree")
	c := filepath.Join(tree, "modified.bin")
	clone(t, orig, c)
	// Rewrite 1 MiB of the clone: only that part becomes private.
	f, err := os.OpenFile(c, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, mb)
	rand.New(rand.NewSource(7)).Read(b)
	if _, err := f.WriteAt(b, mb); err != nil {
		t.Fatal(err)
	}
	f.Close()
	// Two clones of the same data, one of them hardlinked, all inside the tree.
	clone(t, orig, filepath.Join(tree, "c1.bin"))
	clone(t, orig, filepath.Join(tree, "c2.bin"))
	if err := os.MkdirAll(filepath.Join(tree, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(tree, "c1.bin"), filepath.Join(tree, "sub", "c1-link.bin")); err != nil {
		t.Fatal(err)
	}
	st := measure(t, tree)
	// orig (outside) still references the c1/c2 data: only the rewritten MiB is freed.
	if !near(st.Reclaim, 1*mb) {
		t.Errorf("Reclaim = %d, want ~1 MiB", st.Reclaim)
	}
	if !near(st.Bytes, 12*mb) { // modified + c1 (once, hardlinked) + c2
		t.Errorf("Bytes = %d, want ~12 MiB", st.Bytes)
	}
	// Once orig is gone, the tree holds every reference: c1/c2 data counts once.
	if err := os.Remove(orig); err != nil {
		t.Fatal(err)
	}
	st = measure(t, tree)
	if !near(st.Reclaim, 5*mb) {
		t.Errorf("after removing the origin: Reclaim = %d, want ~5 MiB", st.Reclaim)
	}
}

// A clone that was appended to gets its own data stream; its origin then
// shares all of its blocks (EF_SHARES_ALL_BLOCKS) and frees nothing alone.
func TestSizeAppendedClone(t *testing.T) {
	if !useBulk || !useCloneAttrs {
		t.Skip("bulk attributes disabled")
	}
	root := t.TempDir()
	orig := filepath.Join(root, "a", "orig.bin")
	writeRandom(t, orig, 2*mb)
	grown := filepath.Join(root, "b", "grown.bin")
	clone(t, orig, grown)
	f, err := os.OpenFile(grown, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, mb)
	rand.New(rand.NewSource(9)).Read(b)
	if _, err := f.Write(b); err != nil {
		t.Fatal(err)
	}
	f.Close()
	for _, c := range []struct {
		path string
		want int64
	}{
		{filepath.Join(root, "a"), 0},
		{orig, 0},
		{filepath.Join(root, "b"), mb},
		{grown, mb},
	} {
		if st := measure(t, c.path); !near(st.Reclaim, c.want) {
			t.Errorf("%s: Reclaim = %d, want ~%d (Bytes %d)", c.path, st.Reclaim, c.want, st.Bytes)
		}
	}
}

// Transparently compressed files report a private size of 0 (their data is
// in an extended attribute): they must stay fully reclaimable.
func TestSizeCompressedFilesStayReclaimable(t *testing.T) {
	ditto, err := exec.LookPath("ditto")
	if err != nil {
		t.Skip("no ditto")
	}
	root := t.TempDir()
	plain := filepath.Join(root, "plain.txt")
	var text []byte
	for len(text) < 2*mb {
		text = append(text, "some very compressible text, repeated again and again\n"...)
	}
	if err := os.WriteFile(plain, text, 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "c")
	if out, err := exec.Command(ditto, "--hfsCompression", plain, filepath.Join(dir, "comp.txt")).CombinedOutput(); err != nil {
		t.Skipf("ditto: %v %s", err, out)
	}
	st := measure(t, dir)
	if st.Bytes == 0 || st.Reclaim != st.Bytes {
		t.Errorf("compressed: Bytes = %d, Reclaim = %d, want equal and > 0", st.Bytes, st.Reclaim)
	}
	if st := measure(t, filepath.Join(dir, "comp.txt")); st.Reclaim != st.Bytes {
		t.Errorf("compressed root file: Bytes = %d, Reclaim = %d", st.Bytes, st.Reclaim)
	}
}

func TestSizePlainFilesFullyReclaimable(t *testing.T) {
	root := t.TempDir()
	writeRandom(t, filepath.Join(root, "a", "x.bin"), 3*mb)
	writeRandom(t, filepath.Join(root, "b.bin"), mb/2)
	st := measure(t, root)
	if st.Reclaim != st.Bytes || !near(st.Bytes, 3*mb+mb/2) {
		t.Errorf("Bytes = %d, Reclaim = %d", st.Bytes, st.Reclaim)
	}
	if s := measure(t, filepath.Join(root, "b.bin")); s.Reclaim != s.Bytes || s.Bytes == 0 {
		t.Errorf("single file: %+v", s)
	}
}

func TestDecodeBulkTruncated(t *testing.T) {
	var e bulkEntry
	for n := 0; n < 40; n++ {
		rec := make([]byte, n)
		if n >= 24 {
			// every attribute "returned"
			for i := 4; i < 24; i++ {
				rec[i] = 0xff
			}
		}
		decodeBulk(rec, &e) // must not panic
		if n < 24 && !e.hasError {
			t.Errorf("len %d: truncated record not flagged", n)
		}
	}
}

// Inode numbers and clone ids are per volume: a CrossDevice walk must not
// merge two unrelated files of different volumes that happen to share one.
func TestAccountKeysByVolume(t *testing.T) {
	w := &walker{links: map[volKey]*hardlink{}, clones: map[volKey]*cloneFamily{}}
	// Two hardlinked files (nlink 2, one link each in the tree) with the same
	// inode number on two volumes: neither is fully inside the tree.
	w.accountFile(mb, true, 2, 1, 42, fileShare{})
	w.accountFile(mb, true, 2, 2, 42, fileShare{})
	// Two clones (2 references each, one inside the tree) with the same clone id.
	sh := fileShare{known: true, cloneID: 7, refs: 2}
	w.accountFile(2*mb, true, 1, 1, 100, sh)
	w.accountFile(2*mb, true, 1, 2, 101, sh)
	st := w.stats()
	if st.Bytes != 6*mb {
		t.Errorf("Bytes = %d, want %d (both hardlinks and both clones counted)", st.Bytes, 6*mb)
	}
	if st.Reclaim != 0 {
		t.Errorf("Reclaim = %d, want 0: every file shares its data with a file outside the tree", st.Reclaim)
	}
	// Control: on one volume, both links / both clones are inside the tree.
	w = &walker{links: map[volKey]*hardlink{}, clones: map[volKey]*cloneFamily{}}
	w.accountFile(mb, true, 2, 1, 42, fileShare{})
	w.accountFile(mb, true, 2, 1, 42, fileShare{})
	w.accountFile(2*mb, true, 1, 1, 100, sh)
	w.accountFile(2*mb, true, 1, 1, 101, sh)
	if st := w.stats(); st.Bytes != 5*mb || st.Reclaim != 3*mb {
		t.Errorf("same volume: Bytes = %d, Reclaim = %d; want %d, %d", st.Bytes, st.Reclaim, 5*mb, 3*mb)
	}
}

// Once every other copy is gone, a former clone keeps EF_MAY_SHARE_BLOCKS
// while sharing nothing (a node_modules after the bun cache was emptied).
// Fetching the private size of each such file made walks ~10x slower: it is
// only fetched for big files, and the small ones stay fully reclaimable.
func TestSizeFormerClonesSkipPrivateFetch(t *testing.T) {
	if !useBulk || !useCloneAttrs {
		t.Skip("bulk attributes disabled")
	}
	root := t.TempDir()
	src, dst := filepath.Join(root, "cache"), filepath.Join(root, "node_modules")
	for i := 0; i < 50; i++ {
		name := filepath.Join("pkg", "f"+strconv.Itoa(i)+".js")
		writeRandom(t, filepath.Join(src, name), 8<<10+i)
		clone(t, filepath.Join(src, name), filepath.Join(dst, name))
	}
	writeRandom(t, filepath.Join(src, "big.bin"), 2*mb)
	clone(t, filepath.Join(src, "big.bin"), filepath.Join(dst, "big.bin"))
	if err := os.RemoveAll(src); err != nil {
		t.Fatal(err)
	}
	before := privateFetches.Load()
	st := measure(t, dst)
	if st.Reclaim != st.Bytes {
		t.Errorf("Reclaim = %d, want Bytes = %d: nothing is shared any more", st.Reclaim, st.Bytes)
	}
	if n := privateFetches.Load() - before; n > 1 {
		t.Errorf("%d private size fetches, want at most 1 (the big file only)", n)
	}
}

func TestNeedsPrivateOnlyForBigPartlySharedFiles(t *testing.T) {
	base := bulkEntry{objType: vREG, hasClone: true, cloneRefs: 1, extFlags: efMayShareBlocks, alloc: privateMinAlloc}
	if !base.needsPrivate() {
		t.Error("big former clone: private size not fetched")
	}
	for name, e := range map[string]bulkEntry{
		"small":        {objType: vREG, hasClone: true, cloneRefs: 1, extFlags: efMayShareBlocks, alloc: privateMinAlloc - 4096},
		"never shared": {objType: vREG, hasClone: true, cloneRefs: 1, alloc: 1 << 30},
		"all shared":   {objType: vREG, hasClone: true, cloneRefs: 1, extFlags: efMayShareBlocks | efSharesAllBlocks, alloc: 1 << 30},
		"family":       {objType: vREG, hasClone: true, cloneRefs: 2, extFlags: efMayShareBlocks, alloc: 1 << 30},
		"dir":          {objType: vDIR, hasClone: true, cloneRefs: 1, extFlags: efMayShareBlocks, alloc: 1 << 30},
	} {
		if e.needsPrivate() {
			t.Errorf("%s: private size fetched", name)
		}
	}
	// A small partly shared file counts fully (the bounded error).
	small := bulkEntry{objType: vREG, hasClone: true, cloneRefs: 1, extFlags: efMayShareBlocks, alloc: 64 << 10}
	if got := small.share().exclusive(small.alloc); got != small.alloc {
		t.Errorf("small former clone: exclusive = %d, want %d", got, small.alloc)
	}
}
