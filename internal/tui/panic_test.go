package tui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ludwig-pro/lu-cleaner/internal/clean"
	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

func TestSafeCleanContainsWorkerPanic(t *testing.T) {
	if os.Getenv("LU_TEST_CRASH_CHILD") == "1" {
		home := t.TempDir()
		it := &core.Item{ID: "test", Name: "test", Path: filepath.Join(home, "never-deleted"), Risk: core.RiskSafe, Method: core.MethodDelete, Selectable: true}
		it.Recheck = func(context.Context) error { panic("synthetic worker failure") }
		sum := safeClean(clean.Run, context.Background(), []*core.Item{it}, clean.Options{Home: home, DryRun: true, NoHistory: true}, nil)
		if sum.Count(clean.StatusFailed) != 1 {
			t.Fatal("worker failure missing")
		}
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestSafeCleanContainsWorkerPanic$")
	cmd.Env = append(os.Environ(), "LU_TEST_CRASH_CHILD=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("panic escaped cleanup: %v\n%s", err, out)
	}
}

func TestSizerPanicBecomesIncompleteResult(t *testing.T) {
	p := newSizePool(context.Background(), 1)
	defer p.close()
	p.sizeFn = func(context.Context, string) (fsx.Stats, error) { panic("private value") }
	p.push("test")
	result := <-p.out
	if result.err == nil || result.st.Bytes != 0 {
		t.Fatal("failed sizing was presented as successful")
	}
}
