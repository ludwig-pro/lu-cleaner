package artifacts

import (
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

// Kinds lists every kind the provider can emit (the CLI validates -t with
// it): sorted, without duplicates, alternative names included.
func TestKinds(t *testing.T) {
	ks := Kinds()
	if !sort.StringsAreSorted(ks) {
		t.Errorf("not sorted: %v", ks)
	}
	for i := 1; i < len(ks); i++ {
		if ks[i] == ks[i-1] {
			t.Errorf("duplicate kind %q", ks[i])
		}
	}
	for _, k := range []string{
		"node_modules", "ios-pods", "ios-build", "xcode-build", "android-build", "gradle-build",
		"android-gradle", "gradle-cache", "android-kotlin", "gradle-kotlin", "python-venv",
		"python-cache", "cachedir-tag", "extra-artifact", "ignored-dir", "rust-target", "dotnet-build",
	} {
		if !slices.Contains(ks, k) {
			t.Errorf("Kinds() misses %q: %v", k, ks)
		}
	}
	for _, k := range []string{"", "pods", "cocoapods"} {
		if slices.Contains(ks, k) {
			t.Errorf("Kinds() has %q (aliases are the CLI's business)", k)
		}
	}
	// Callers may modify the slice.
	ks[0] = "changed"
	if Kinds()[0] == "changed" {
		t.Error("Kinds() returns shared state")
	}

	// Every kind a real scan emits is listed.
	f := rnMonorepo(t)
	others(f)
	_, items := f.scan()
	seen := map[string]bool{}
	for _, it := range items {
		seen[it.Kind] = true
		if !slices.Contains(Kinds(), it.Kind) {
			t.Errorf("emitted kind %q (%s) missing from Kinds()", it.Kind, it.ID)
		}
	}
	if len(seen) < 10 {
		t.Errorf("fixture emitted too few kinds to be meaningful: %v", seen)
	}
}

// Deleting an artifact whose data is shared with other files frees less than
// its size: the note says it without claiming the sharing is a hardlink (bun
// and pnpm clone files on APFS).
func TestReclaimNoteWording(t *testing.T) {
	for pm, want := range map[string]string{
		"pnpm":       " of the pnpm store",
		"bun":        " of the bun cache",
		"yarn-berry": " of the Yarn global cache",
		"npm":        "",
		"":           "",
	} {
		n := reclaimNote(pm, 4096)
		full := "Shared with other files (hardlinks or APFS clones" + want + "): only " + fsx.Bytes(4096) + " is really freed."
		if n != full {
			t.Errorf("%q: note = %q, want %q", pm, n, full)
		}
		if strings.Contains(strings.ToLower(n), "hardlinked with") {
			t.Errorf("%q: old wording: %q", pm, n)
		}
	}

	// In a scan: the plain npm node_modules of the fixture is partly hardlinked.
	f := rnMonorepo(t)
	items, _ := f.scan()
	pn := items[f.path("src/plain/node_modules")]
	if pn == nil {
		t.Fatal("no src/plain/node_modules item")
	}
	if pn.Reclaim <= 0 || pn.Reclaim >= pn.Size {
		t.Fatalf("fixture no longer shares data: size=%d reclaim=%d", pn.Size, pn.Reclaim)
	}
	if !strings.Contains(pn.Note, "Shared with other files (hardlinks or APFS clones): only ") || strings.Contains(pn.Note, "Hardlinked") {
		t.Errorf("note = %q", pn.Note)
	}
}
