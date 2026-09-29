package aitools

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

// publishOne runs scanner.publish on a single-folder item and returns the
// final emitted version.
func (f *fixture) publishOne(dir string) *core.Item {
	f.t.Helper()
	var mu sync.Mutex
	var last *core.Item
	s := newScanner(context.Background(), f.p, f.env, func(it *core.Item) {
		mu.Lock()
		last = it
		mu.Unlock()
	})
	it := s.newItem("reclaim-test", core.CatAI, "reclaim test", core.RiskSafe)
	it.ID = itemID("reclaim-test", dir)
	it.Note = "test"
	it.Path = dir
	s.publish(it, pubOpts{})
	if last == nil {
		f.t.Fatalf("%s: nothing emitted", dir)
	}
	return last
}

// A tree whose every file is shared with a file outside it (hardlinks or
// APFS clones) frees ~nothing: fsx reports Reclaim 0, which must not be
// stored as 0 ("same as Size") but as the 1-byte sentinel (SetReclaim).
func TestPublishFullySharedTreeFreesNothing(t *testing.T) {
	f := newFixture(t)
	outside := f.file("elsewhere/blob.bin", 512<<10, 0)
	dir := f.dir("Library/Caches/ai-shared", 0)
	if err := os.Link(outside, dir+"/blob.bin"); err != nil {
		t.Skipf("hardlinks not supported here: %v", err)
	}

	it := f.publishOne(dir)
	if it.Size == 0 {
		t.Fatalf("Size = 0, want the hardlinked file's size")
	}
	if it.Reclaim != 1 {
		t.Errorf("Reclaim = %d, want 1 (fully shared: nothing really freed)", it.Reclaim)
	}
	if got := it.Freed(); got >= it.Size {
		t.Errorf("Freed() = %d, want < Size %d", got, it.Size)
	}
}

// A plain tree keeps Reclaim 0 ("same as Size").
func TestPublishPlainTreeFreesItsSize(t *testing.T) {
	f := newFixture(t)
	dir := f.dir("Library/Caches/ai-plain", 0)
	f.file("Library/Caches/ai-plain/blob.bin", 512<<10, 0)

	it := f.publishOne(dir)
	if it.Size == 0 || it.Reclaim != 0 || it.Freed() != it.Size {
		t.Errorf("Size = %d, Reclaim = %d, Freed = %d: want Reclaim 0 and Freed == Size", it.Size, it.Reclaim, it.Freed())
	}
}

// A partly shared tree reports only its private bytes.
func TestPublishPartlySharedTree(t *testing.T) {
	f := newFixture(t)
	outside := f.file("elsewhere/blob.bin", 512<<10, 0)
	dir := f.dir("Library/Caches/ai-mixed", 0)
	f.file("Library/Caches/ai-mixed/own.bin", 256<<10, 0)
	if err := os.Link(outside, dir+"/shared.bin"); err != nil {
		t.Skipf("hardlinks not supported here: %v", err)
	}

	it := f.publishOne(dir)
	if it.Reclaim <= 1 || it.Reclaim >= it.Size {
		t.Errorf("Size = %d, Reclaim = %d: want 1 < Reclaim < Size", it.Size, it.Reclaim)
	}
}
