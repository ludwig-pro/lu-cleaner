package apple

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

// Regression tests for the integration leftovers (A1-A3).

// A1: the deletable areas mirror the safety guard (safety.TempAreas). An
// unset or shared TMPDIR (/tmp) used to make its parent "/" internal, and a
// private TMPDIR its parent folder.
func TestPlaceMirrorsGuardTempAreas(t *testing.T) {
	f := newFixture(t)
	f.env.TmpDir = "/tmp"
	s := newScan(t.Context(), f.prov, f.env, func(*core.Item) {})
	if got := s.place("/private/tmp"); got == placeInternal {
		t.Errorf("TMPDIR=/tmp must not make /private/tmp deletable (areas %v)", s.tmpAreas)
	}
	if got := s.place(f.dir("Library/x", ago(day))); got != placeInternal {
		t.Errorf("home path: %v", got)
	}

	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(base, "T")
	inside, sibling := filepath.Join(tmp, "x"), filepath.Join(base, "y")
	for _, d := range []string{inside, sibling} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	os.Chmod(tmp, 0o700)
	f.env.TmpDir = tmp
	s = newScan(t.Context(), f.prov, f.env, func(*core.Item) {})
	if got := s.place(inside); got != placeInternal {
		t.Errorf("inside TMPDIR: %v (areas %v)", got, s.tmpAreas)
	}
	if got := s.place(sibling); got != placeOutside {
		t.Errorf("next to TMPDIR: %v (areas %v)", got, s.tmpAreas)
	}
	out := &core.Item{Method: core.MethodDelete, Selectable: true}
	if !s.applyPlace(out, sibling) || out.Method != core.MethodReport || out.CanClean() {
		t.Errorf("a path next to TMPDIR cannot be deleted by lu-cleaner: %+v", out)
	}
}

// A2: a tree fully shared with other files (hardlinks or APFS clones) frees
// about nothing: Reclaim must not be 0, which means "same as Size".
func TestMeasuredApplyFullyShared(t *testing.T) {
	for _, tc := range []struct{ bytes, reclaim, want int64 }{
		{1 << 20, 0, 1},
		{1 << 20, 4096, 4096},
		{1 << 20, 1 << 20, 0},
		{0, 0, 0},
	} {
		it := &core.Item{Reclaim: 123, Sizing: true}
		measured{bytes: tc.bytes, reclaim: tc.reclaim, files: 2}.apply(it)
		if it.Sizing || it.Size != tc.bytes || it.Files != 2 || it.Reclaim != tc.want {
			t.Errorf("bytes %d reclaim %d: %+v, want Reclaim %d", tc.bytes, tc.reclaim, it, tc.want)
		}
	}
}

// A3: the keep_latest newest runtimes of each platform stay out of smart
// selection; several images of one runtime count once.
func TestSimulatorRuntimesKeepLatest(t *testing.T) {
	const (
		img175 = "5EB08DF5-AB86-44F4-9DC7-D09DAF3F9352" // iOS 17.5, unused, 100 days
		img184 = "5EB08DF5-AB86-44F4-9DC7-D09DAF3F9353" // iOS 18.4, newest
		dup184 = "5EB08DF5-AB86-44F4-9DC7-D09DAF3F9355" // a second image of iOS 18.4
	)
	run := func(keep int) *scanResult {
		f := newSimFixture(t)
		f.build()
		f.addRuntimeImage(dup184, runtimeImage{
			Identifier: dup184, RuntimeIdentifier: "com.apple.CoreSimulator.SimRuntime.iOS-18-4", Version: "18.4", Build: "22E238",
			Kind: "Disk Image", State: "Ready", Deletable: true, SizeBytes: 8_500_000_000,
			LastUsedAt: ago(100 * day).Format(time.RFC3339), PlatformIdentifier: "com.apple.platform.iphonesimulator",
		})
		f.env.KeepLatest = keep
		return f.scan()
	}
	get := func(r *scanResult, id string) *core.Item {
		t.Helper()
		it := r.final["apple:ios-simulator-runtime:"+id]
		if it == nil {
			t.Fatalf("missing runtime %s", id)
		}
		return it
	}

	// keep_latest unset / 1 / 2: 18.4 and 18.2 are the two newest runtimes,
	// 17.5 (the third) is still proposed.
	for _, keep := range []int{0, 1, 2} {
		r := run(keep)
		old := get(r, img175)
		if !old.Recommended || old.NoRecommend || old.Meta["kept"] != "" || !core.Recommend(old, now, 14*day) {
			t.Errorf("keep_latest %d: the third newest runtime must be recommended: %+v", keep, old)
		}
		newest := get(r, img184)
		if !strings.Contains(newest.Warn, "newest iOS runtime") || !newest.NoRecommend || core.Recommend(newest, now, 14*day) {
			t.Errorf("keep_latest %d: newest runtime: %+v", keep, newest)
		}
		// The other image of the newest runtime is kept too (old enough to be
		// picked by age otherwise).
		dup := get(r, dup184)
		if !dup.NoRecommend || dup.Recommended || core.Recommend(dup, now, 14*day) || dup.Meta["kept"] == "" {
			t.Errorf("keep_latest %d: second image of the newest runtime: %+v", keep, dup)
		}
	}

	// keep_latest 3: 17.5 is the third runtime (not the fourth image).
	r := run(3)
	old := get(r, img175)
	if old.Recommended || !old.NoRecommend || core.Recommend(old, now, 14*day) ||
		!strings.Contains(old.Meta["kept"], "3 newest installed iOS runtimes") || !strings.Contains(old.Note, "keep_latest") {
		t.Errorf("keep_latest 3 keeps the third newest runtime: %+v", old)
	}
	if old.Method != core.MethodCommand || !old.CanClean() || old.Warn != "" {
		t.Errorf("a kept runtime stays cleanable by hand, without a warning: %+v", old)
	}
	// Bundled runtimes of another platform are unaffected.
	if w := get(r, "5EB08DF5-AB86-44F4-9DC7-D09DAF3F9354"); w.Method != core.MethodReport || w.CanClean() {
		t.Errorf("bundled watchOS runtime: %+v", w)
	}
}
