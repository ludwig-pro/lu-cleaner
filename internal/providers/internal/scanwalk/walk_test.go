package scanwalk

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestWalkDirKeepsTraversalAndSkipSemantics(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"a/one", "a/two", "b/one", "c"} {
		p = filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "b"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	for _, skip := range []string{"", "b", "a/one", "a"} {
		t.Run(skip, func(t *testing.T) {
			collect := func(out *[]string) fs.WalkDirFunc {
				return func(p string, d fs.DirEntry, err error) error {
					if err != nil {
						return err
					}
					rel, _ := filepath.Rel(root, p)
					*out = append(*out, rel)
					if rel == skip {
						if skip == "a" {
							return fs.SkipAll
						}
						return fs.SkipDir
					}
					return nil
				}
			}
			var want, got []string
			if err := filepath.WalkDir(root, collect(&want)); err != nil {
				t.Fatal(err)
			}
			if err := WalkDir(context.Background(), root, collect(&got)); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("walk = %v, want %v", got, want)
			}
		})
	}
}

func TestWalkDirCancellationCannotBeIgnoredByCallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	err := WalkDir(ctx, t.TempDir(), func(_ string, _ fs.DirEntry, _ error) error {
		calls++
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("walk cancelled: calls=%d, error=%v", calls, err)
	}
}
