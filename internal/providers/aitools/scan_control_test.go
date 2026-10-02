package aitools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

func TestCancelledResolverNeverReportsMissingFromCache(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "present"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := newResolver(claudeEncode)
	r.root = root
	encoded := claudeEncode(filepath.Join(root, "present", "missing"))
	if _, ex := r.resolve(context.Background(), encoded); ex != existNo {
		t.Fatalf("fixture must be provably missing: %v", ex)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ex := r.resolve(ctx, encoded); ex != existUnknown {
		t.Fatalf("cancelled resolver = %v, want unknown", ex)
	}
}

func TestResolverListingKeepsSortedCachedDirectories(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"b", "a"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	r := newResolver(claudeEncode)
	listing := r.list(t.Context(), root)
	if listing.err != nil || len(listing.names) != 2 || listing.names[0] != "a" || listing.names[1] != "b" {
		t.Fatalf("listing = %+v", listing)
	}
	if err := os.Mkdir(filepath.Join(root, "c"), 0o755); err != nil {
		t.Fatal(err)
	}
	if cached := r.list(t.Context(), root); cached != listing {
		t.Fatal("directory listing was not shared from the cache")
	}
}

func TestCancelledProcessInspectionKeepsOldBinary(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := &Provider{
		execInside: func(string) bool { t.Fatal("cancelled scan called executable probe"); return false },
		running:    func(...string) []string { t.Fatal("cancelled scan called running probe"); return nil },
	}
	s := newScanner(ctx, p, &core.Env{Home: t.TempDir()}, nil)
	if !s.runningWithin("/unused-version") {
		t.Fatal("incomplete process inspection marked a binary unused")
	}
	if _, err := s.runningNames("claude"); !errors.Is(err, context.Canceled) {
		t.Fatalf("running probe = %v, want cancellation", err)
	}
}

func TestWorkspaceOrphanRecheckCancellation(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "workspace.json"), []byte(`{"folder":"/removed-project"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := workspaceOrphanRecheck([]string{dir})(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled recheck = %v, want cancellation", err)
	}
}
