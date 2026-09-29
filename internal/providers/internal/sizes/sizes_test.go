package sizes

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

func write(t *testing.T, p string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPrefetcher(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	var dirs []string
	for _, d := range []string{"a", "b", "c"} {
		write(t, filepath.Join(root, d, "x"), 10_000)
		dirs = append(dirs, filepath.Join(root, d))
	}
	ctx := fsx.WithCache(context.Background())
	p := NewPrefetcher(ctx, 2)
	for _, d := range dirs {
		p.Add(d)
		p.Add(d) // once
	}
	// Close waits for the walks in flight but drops what did not start: wait
	// for the queue to drain before closing, then check the cache holds them.
	for {
		p.mu.Lock()
		n := len(p.queue)
		p.mu.Unlock()
		if n == 0 {
			break
		}
		runtime.Gosched()
	}
	p.Close()
	p.Close()
	p.Add(filepath.Join(root, "late")) // ignored once closed
	for _, d := range dirs {
		write(t, filepath.Join(d, "late"), 100_000)
		if st, _ := fsx.Size(ctx, d, nil); st.Bytes >= 100_000 {
			t.Errorf("%s was not prefetched: walked again (%d bytes)", d, st.Bytes)
		}
	}
}

func TestPrefetcherCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := NewPrefetcher(ctx, 3)
	cancel()
	p.Add("/nonexistent/a")
	p.Close() // must not hang
}
