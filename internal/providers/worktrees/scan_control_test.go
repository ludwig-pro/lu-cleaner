package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/scanctl"
)

func TestSizeCheckoutMarkersWaitForIOAndCancel(t *testing.T) {
	child := t.TempDir()
	if err := os.WriteFile(filepath.Join(child, ".git"), []byte("gitdir: /elsewhere"), 0o600); err != nil {
		t.Fatal(err)
	}
	limits, _, _ := scanctl.Resolve("fast", "1")
	controller := scanctl.New(limits)
	owner := scanctl.With(t.Context(), controller)
	release, err := controller.AcquireIO(owner)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(owner, 20*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := nestedCheckout(ctx, child); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("blocked marker inspection = %v, want cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("worktree marker inspection did not cancel")
	}
}

func TestWorktreeSizeCallbackWithSingleIOWorker(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, ".git"), []byte("gitdir: /elsewhere"), 0o600); err != nil {
		t.Fatal(err)
	}
	limits, _, _ := scanctl.Resolve("fast", "1")
	controller := scanctl.New(limits)
	ctx, cancel := context.WithTimeout(scanctl.With(t.Context(), controller), time.Second)
	defer cancel()
	result := measure(ctx, root)
	if ctx.Err() != nil {
		t.Fatal(ctx.Err())
	}
	if len(result.nested) != 1 || result.nested[0] != child {
		t.Fatalf("nested checkout = %v, want %s", result.nested, child)
	}
	if snapshot := controller.Snapshot(); snapshot.IOMax != 1 || snapshot.IOActive != 0 {
		t.Fatalf("worktree size callback bypassed or leaked admission: %+v", snapshot)
	}
}
