package engine

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsevents"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

// sizer emits one item per path, measured with fsx.Size.
type sizer struct{ paths []string }

func (sizer) ID() string                  { return "sizer" }
func (sizer) Title() string               { return "sizer" }
func (sizer) Categories() []core.Category { return nil }
func (p sizer) Scan(ctx context.Context, env *core.Env, emit core.Emit) error {
	for _, path := range p.paths {
		st, err := fsx.Size(ctx, path, nil)
		if err != nil {
			continue
		}
		emit(&core.Item{ID: path, Name: filepath.Base(path), Path: path, Size: st.Bytes, Files: st.Files})
	}
	return nil
}

func writeFiles(t *testing.T, dir string, n, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "pkg", "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range n {
		if err := os.WriteFile(filepath.Join(dir, "pkg", "lib", "f"+strconv.Itoa(i)), make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// A second scan answers unchanged trees from the persistent size cache, and
// re-walks what changed since, deletions included (after a clean).
func TestRunUsesPersistentSizeCache(t *testing.T) {
	if !fsevents.Supported() {
		t.Skip("FSEvents unavailable in this build")
	}
	old := fsx.SizeCacheDir
	fsx.SizeCacheDir = t.TempDir()
	t.Cleanup(func() { fsx.SizeCacheDir = old })
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("LU_NO_CACHE", "")

	a := filepath.Join(home, "a", "node_modules")
	b := filepath.Join(home, "Library", "Caches", "b")
	writeFiles(t, a, 50, 4096)
	writeFiles(t, b, 50, 4096)
	prov := []core.Provider{sizer{paths: []string{a, b}}}
	scan := func() (map[string]int64, int64) {
		t.Helper()
		w0, _ := fsx.Walked()
		res := Collect(context.Background(), &core.Env{}, prov, nil)
		w1, _ := fsx.Walked()
		sizes := map[string]int64{}
		for _, it := range res.Items {
			sizes[it.Path] = it.Size
		}
		return sizes, w1 - w0
	}

	first, walked := scan()
	if walked != 2 {
		t.Fatalf("first scan walked %d trees, want 2", walked)
	}
	second, walked := scan()
	if walked != 0 {
		t.Errorf("second scan walked %d trees, want 0 (nothing changed)", walked)
	}
	if second[a] != first[a] || second[b] != first[b] {
		t.Errorf("cached sizes %v, want %v", second, first)
	}

	// A clean deletes part of b, then all of a.
	if err := os.RemoveAll(filepath.Join(b, "pkg", "lib")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(a); err != nil {
		t.Fatal(err)
	}
	third, walked := scan()
	if walked != 1 {
		t.Errorf("scan after the clean walked %d trees, want 1 (b; a is gone)", walked)
	}
	if _, ok := third[a]; ok {
		t.Errorf("deleted tree still reported: %v", third)
	}
	if third[b] >= first[b] {
		t.Errorf("b = %d after deleting inside it, want less than %d", third[b], first[b])
	}

	t.Setenv("LU_NO_CACHE", "1")
	if _, walked := scan(); walked != 1 {
		t.Errorf("LU_NO_CACHE=1: walked %d trees, want 1", walked)
	}
}
