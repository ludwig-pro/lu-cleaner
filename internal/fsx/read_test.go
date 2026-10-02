package fsx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestControlledReadAndGlob(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "ab"), 1)
	write(t, filepath.Join(root, "a_b"), 1)
	write(t, filepath.Join(root, "b", "two"), 1)
	write(t, filepath.Join(root, "a", "one"), 1)
	if err := os.Symlink(filepath.Join(root, "a"), filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	ctx, c := controlled(t, context.Background(), "1")
	defer c.Close()
	for _, pat := range []string{"*", "*/*", "a/one", "missing", "[ab]/*", "*/missing", "[", "linked/*", `a\b`, `a\_b`, "missing*/../*", "a*/../*"} {
		pattern := filepath.Join(root, pat)
		want, we := filepath.Glob(pattern)
		got, ge := Glob(ctx, pattern)
		if !reflect.DeepEqual(got, want) || !errors.Is(ge, we) {
			t.Fatalf("%q got %v %v want %v %v", pat, got, ge, want, we)
		}
	}
	entries, err := ReadDir(ctx, root)
	if err != nil || entries[0].Name() != "a" || entries[1].Name() != "a_b" {
		t.Fatalf("sorted entries %v %v", entries, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := Glob(canceled, filepath.Join(root, "*")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestReadFileAdmittedAndCanceled(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "metadata.json")
	content := make([]byte, 200<<10)
	for i := range content {
		content[i] = byte(i)
	}
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, c := controlled(t, context.Background(), "1")
	defer c.Close()
	got, err := ReadFile(ctx, path)
	if err != nil || !reflect.DeepEqual(got, content) {
		t.Fatalf("complete read: %d %v", len(got), err)
	}
	release, err := c.AcquireIO(ctx)
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		b, e := ReadFile(canceled, path)
		if len(b) != 0 {
			t.Error("partial input published")
		}
		done <- e
	}()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	release()
	if s := c.Snapshot(); s.IOMax != 1 || s.IOActive != 0 {
		t.Fatalf("file reads unbounded %+v", s)
	}
}
