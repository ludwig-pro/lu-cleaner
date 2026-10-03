package aitools

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

func TestSubscanPanicIsReported(t *testing.T) {
	f := newFixture(t)
	f.file(".local/share/claude/versions/2.1.278", 1000, 20*day)
	f.file(".local/share/claude/versions/2.1.283", 1000, day)
	f.link(".local/bin/claude", f.path(".local/share/claude/versions/2.1.283"))
	var called atomic.Bool
	err := f.p.Scan(context.Background(), f.env, func(*core.Item) { called.Store(true); panic("synthetic failure") })
	if !called.Load() {
		t.Fatal("fixture did not exercise emit")
	}
	if err == nil {
		t.Fatal("subscan panic became a successful partial scan")
	}
}
