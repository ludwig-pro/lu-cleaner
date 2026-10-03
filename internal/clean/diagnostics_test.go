package clean

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ludwig-pro/lu-cleaner/internal/config"
	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

func TestWorkerPanicStopsPendingCleanup(t *testing.T) {
	f := newFixture(t)
	a := item(filepath.Join(f.home, "src", "a"), core.MethodDelete)
	b := item(filepath.Join(f.home, "src", "b"), core.MethodDelete)
	a.Recheck = func(context.Context) error { panic("/Users/private/token=secret") }
	b.Recheck = func(context.Context) error { t.Error("pending cleanup started after internal failure"); return nil }
	opt := f.opts()
	opt.DryRun, opt.Parallel = true, 1
	sum := Run(context.Background(), []*core.Item{a, b}, opt, nil)
	if sum.Count(StatusFailed) != 1 || sum.Count(StatusSkipped) != 1 || sum.InternalError == nil {
		t.Fatalf("unexpected summary: %+v", sum)
	}
	if sum.Estimated != 0 || strings.Contains(sum.Results[0].Error, "private") {
		t.Fatal("invented bytes or unsafe panic value")
	}
}

func TestSerialPanicIsContained(t *testing.T) {
	f := newFixture(t)
	a := item("", core.MethodCommand)
	a.Recheck = func(context.Context) error { panic("serial failure") }
	opt := f.opts()
	opt.DryRun = true
	sum := Run(context.Background(), []*core.Item{a}, opt, nil)
	if sum.Count(StatusFailed) != 1 || sum.InternalError == nil {
		t.Fatal("serial panic not contained")
	}
}

func TestProgressPanicPreservesCompletedAccounting(t *testing.T) {
	f := newFixture(t)
	p := f.mk(t, "src/app/cache", "payload")
	it := item(p, core.MethodDelete)
	opt := f.opts()
	opt.Force = true
	sum := Run(context.Background(), []*core.Item{it}, opt, func(Result) { panic("callback failure") })
	if sum.InternalError == nil || sum.Count(StatusDone) != 1 || sum.Estimated != it.Size {
		t.Fatalf("completed cleanup lost: %+v", sum)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("fixture not deleted: %v", err)
	}
}

func TestHistoryPrivateRotatedAndFailuresVisible(t *testing.T) {
	f := newFixture(t)
	p := f.mk(t, "src/app/cache", "payload")
	sum := &Summary{Results: []Result{{Item: item(p, core.MethodDelete), Status: StatusDone}}}
	if err := appendHistory(sum); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(config.StateDir()); st.Mode().Perm() != 0o700 {
		t.Fatal("history directory public")
	}
	if st, _ := os.Stat(HistoryPath()); st.Mode().Perm() != 0o600 {
		t.Fatal("history public")
	}
	file, err := os.OpenFile(HistoryPath(), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := file.Stat()
	if err := file.Truncate(maxHistoryBytes - 1); err != nil {
		t.Fatal(err)
	}
	_ = st
	file.Close()
	if err := appendHistory(sum); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"history.jsonl", "history.previous.jsonl"} {
		st, err := os.Stat(filepath.Join(config.StateDir(), name))
		if err != nil || st.Size() > maxHistoryBytes {
			t.Fatalf("history unbounded: %s %v", name, err)
		}
	}
	// An unsafe history destination must warn, without changing a deletion's result.
	if err := os.Remove(HistoryPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(f.home, "unrelated"), HistoryPath()); err != nil {
		t.Fatal(err)
	}
	opt := f.opts()
	opt.NoHistory, opt.Force = false, true
	result := Run(context.Background(), []*core.Item{item(p, core.MethodDelete)}, opt, nil)
	if result.HistoryError == nil || result.Count(StatusDone) != 1 {
		t.Fatalf("history failure hid cleanup: %+v", result)
	}
}
