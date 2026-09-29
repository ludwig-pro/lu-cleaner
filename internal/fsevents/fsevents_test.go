package fsevents

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// realTempDir returns a temporary directory spelled as FSEvents reports it
// (/private/var/..., not /var/...).
func realTempDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func requireSupported(t *testing.T) {
	t.Helper()
	if !Supported() {
		t.Skip("FSEvents unavailable in this build (cgo disabled or not macOS)")
	}
}

func TestIdsAndVolume(t *testing.T) {
	requireSupported(t)
	dir := realTempDir(t)
	if CurrentEventID() == 0 {
		t.Fatal("CurrentEventID = 0")
	}
	if VolumeUUID(dir) == "" {
		t.Fatal("VolumeUUID empty")
	}
	if VolumeUUID(filepath.Join(dir, "missing")) != "" {
		t.Error("VolumeUUID of a missing path should be empty")
	}
}

func TestChangedSinceSeesChangesMadeJustBefore(t *testing.T) {
	requireSupported(t)
	dir := realTempDir(t)
	other := realTempDir(t)
	since := CurrentEventID()
	a := filepath.Join(dir, "sub", "a.txt")
	os.MkdirAll(filepath.Dir(a), 0o755)
	os.WriteFile(a, []byte("x"), 0o644)
	os.WriteFile(filepath.Join(other, "elsewhere"), nil, 0o644)
	// No delay: the marker makes Changes wait for these events.
	paths, rescan, ok := ChangedSince(context.Background(), []string{dir}, since, 5*time.Second)
	if !ok || rescan {
		t.Fatalf("ok=%v rescan=%v", ok, rescan)
	}
	if !slices.Contains(paths, a) || !slices.Contains(paths, filepath.Join(dir, "sub")) {
		t.Errorf("changes = %q, want %s and its directory", paths, a)
	}
	for _, p := range paths {
		if !coveredBy(p, []string{dir}) {
			t.Errorf("event outside the roots: %s", p)
		}
	}
	// Nothing changed since now.
	since = CurrentEventID()
	paths, _, ok = ChangedSince(context.Background(), []string{dir}, since, 5*time.Second)
	if !ok || len(paths) != 0 {
		t.Errorf("no change expected, got ok=%v %q", ok, paths)
	}
}

func TestChangesReportsRenames(t *testing.T) {
	requireSupported(t)
	dir := realTempDir(t)
	os.MkdirAll(filepath.Join(dir, "proj", "node_modules"), 0o755)
	since := CurrentEventID()
	if err := os.Rename(filepath.Join(dir, "proj"), filepath.Join(dir, "proj2")); err != nil {
		t.Fatal(err)
	}
	var got []Event
	ok := Changes(context.Background(), []string{dir}, since, 5*time.Second, func(e Event) bool {
		got = append(got, e)
		return true
	})
	if !ok {
		t.Fatal("replay failed")
	}
	var old bool
	for _, e := range got {
		if e.Path == filepath.Join(dir, "proj") && e.Flags&FlagItemRenamed != 0 {
			old = true
		}
	}
	if !old {
		t.Errorf("the old path of a renamed directory must be reported: %+v", got)
	}
}

func TestChangesStopAndCancel(t *testing.T) {
	requireSupported(t)
	dir := realTempDir(t)
	since := CurrentEventID()
	for i := range 5 {
		os.WriteFile(filepath.Join(dir, string(rune('a'+i))), nil, 0o644)
	}
	n := 0
	ok := Changes(context.Background(), []string{dir}, since, 5*time.Second, func(e Event) bool {
		n++
		return false
	})
	if !ok || n != 1 {
		t.Errorf("stopped replay: ok=%v after %d events, want true after 1", ok, n)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if Changes(ctx, []string{dir}, since, 5*time.Second, func(Event) bool { return true }) {
		t.Error("a cancelled replay must report ok=false")
	}
	if Changes(context.Background(), []string{dir}, CurrentEventID()+1_000_000, time.Second, func(Event) bool { return true }) {
		t.Error("an id from the future must report ok=false")
	}
	if Changes(context.Background(), []string{dir}, 0, time.Second, func(Event) bool { return true }) {
		t.Error("since=0 must report ok=false")
	}
}

func TestSync(t *testing.T) {
	requireSupported(t)
	dir := realTempDir(t)
	a := filepath.Join(dir, "a")
	os.WriteFile(a, nil, 0o644)
	id, ok := Sync(context.Background(), 5*time.Second)
	if !ok || id == 0 {
		t.Fatalf("Sync: %d %v", id, ok)
	}
	b := filepath.Join(dir, "b")
	os.WriteFile(b, nil, 0o644)
	paths, rescan, ok := ChangedSince(context.Background(), []string{dir}, id, 5*time.Second)
	if !ok || rescan || slices.Contains(paths, a) || !slices.Contains(paths, b) {
		t.Errorf("changes since Sync = %q (ok %v, rescan %v), want %s only", paths, ok, rescan, b)
	}
}

func TestCoveredBy(t *testing.T) {
	roots := []string{"/a/b", "/x"}
	for p, want := range map[string]bool{"/a/b": true, "/a/b/c": true, "/a/bc": false, "/a": false, "/x/y": true, "/y": false} {
		if got := coveredBy(p, roots); got != want {
			t.Errorf("coveredBy(%s) = %v", p, got)
		}
	}
	if !coveredBy("/anything", []string{"/"}) {
		t.Error("/ covers everything")
	}
	if !above("/a", roots) || above("/a/b", roots) || above("/a/bc", roots) || above("/z", roots) {
		t.Error("above: only strict ancestors of a root")
	}
}

// Without cgo (or on another OS) nothing is available and callers fall back
// to full walks.
func TestUnsupportedBuild(t *testing.T) {
	if Supported() {
		t.Skip("FSEvents available in this build")
	}
	if CurrentEventID() != 0 || VolumeUUID("/") != "" {
		t.Error("stub must report nothing")
	}
	if Changes(context.Background(), []string{"/"}, 1, time.Second, func(Event) bool { return true }) {
		t.Error("stub Changes must report ok=false")
	}
	if _, ok := Sync(context.Background(), time.Second); ok {
		t.Error("stub Sync must report ok=false")
	}
}
