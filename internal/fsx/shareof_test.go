package fsx

import (
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// A file measured on its own fetches its private size (a walk of its
// extents, seconds on a big fragmented VM image or database) only when it
// may share part of its blocks, like a file met during a walk.
func TestSizeSingleFilePrivateFetch(t *testing.T) {
	if !useBulk || !useCloneAttrs {
		t.Skip("bulk attributes disabled")
	}
	root := t.TempDir()
	plain := filepath.Join(root, "plain.img")
	writeRandom(t, plain, 4*mb)
	before := privateFetches.Load()
	if st := measure(t, plain); st.Reclaim != st.Bytes {
		t.Errorf("never cloned: Reclaim = %d, want Bytes = %d", st.Reclaim, st.Bytes)
	}
	if n := privateFetches.Load() - before; n != 0 {
		t.Errorf("%d private size fetches for a file never cloned, want 0", n)
	}

	// A clone rewritten in part: its private bytes are needed.
	orig := filepath.Join(root, "orig.img")
	writeRandom(t, orig, 4*mb)
	c := filepath.Join(root, "clone.img")
	clone(t, orig, c)
	f, err := os.OpenFile(c, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, mb)
	rand.New(rand.NewSource(3)).Read(b)
	if _, err := f.WriteAt(b, mb); err != nil {
		t.Fatal(err)
	}
	f.Close()
	before = privateFetches.Load()
	st := measure(t, c)
	if privateFetches.Load() == before {
		t.Error("partly shared clone: private size not fetched")
	}
	if !near(st.Reclaim, 1*mb) {
		t.Errorf("partly shared clone: Reclaim = %d, want ~1 MiB", st.Reclaim)
	}
}
