package fsx

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/fsevents"
	"github.com/ludwig-pro/lu-cleaner/internal/scanctl"
)

func TestStoreCloseCancelsWriterInspectionAndPreservesHistory(t *testing.T) {
	oldWriters, oldReplay := listWriters, replayChanges
	t.Cleanup(func() { listWriters, replayChanges = oldWriters, oldReplay })
	started := make(chan struct{})
	listWriters = func(ctx context.Context) ([]string, bool, error) {
		close(started)
		<-ctx.Done()
		return []string{"/partial/db"}, false, ctx.Err()
	}
	replayChanges = func(context.Context, []string, uint64, time.Duration, func(fsevents.Event) bool) bool {
		t.Error("an incomplete writer inspection must not validate the history")
		return true
	}
	ctx, cancel := context.WithCancel(scanctl.Ensure(context.Background()))
	defer cancel()
	entry := &storedSize{Path: "/cached/tree", Dir: "/cached", Real: "/cached/tree", EventID: 7, Measured: time.Now().UnixNano()}
	expired := &storedSize{Path: "/expired/tree", Dir: "/expired", Real: "/expired/tree", EventID: 7, Measured: time.Now().Add(-8 * 24 * time.Hour).UnixNano()}
	s := &SizeStore{
		file: filepath.Join(t.TempDir(), storeFileName), home: "/cached", volume: "test-volume", boot: "test-boot",
		runID: 9, since: 7, newest: 7, cancel: cancel, ready: make(chan struct{}),
		entries: []*storedSize{entry, expired}, loaded: map[string]*storedSize{entry.Path: entry},
		valid: map[*storedSize]bool{}, fresh: map[string]*storedSize{},
	}
	go s.validate(ctx)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("writer inspection did not start")
	}
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not join the cancelled writer inspection")
	}
	if s.state != replayCancelled || len(s.valid) != 0 {
		t.Fatalf("state=%s valid=%d; cancellation must preserve an unknown history", stateName(s.state), len(s.valid))
	}
	f, ok := decodeStore(mustRead(t, s.file))
	if !ok || len(f.Entries) != 1 || f.Entries[0].EventID != 7 || f.Entries[0].Measured != entry.Measured {
		t.Fatalf("cancelled validation advanced or lost the history: %+v", f.Entries)
	}
}

func TestCancelledCacheLookupDoesNotInvalidateEntry(t *testing.T) {
	entry := &storedSize{Path: "/cached/tree"}
	ready := make(chan struct{})
	close(ready)
	s := &SizeStore{loaded: map[string]*storedSize{entry.Path: entry}, valid: map[*storedSize]bool{entry: true}, ready: ready}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := s.lookup(ctx, entry.Path); ok {
		t.Fatal("cancelled caller received a cache hit")
	}
	if !s.valid[entry] {
		t.Fatal("consumer cancellation invalidated a shared entry")
	}
}

func TestRootIdentityHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := identifyContext(ctx, t.TempDir()); ok || !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("root identified despite cancellation")
	}
}
