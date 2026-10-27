package fsx

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/fsevents"
	"github.com/ludwig-pro/lu-cleaner/internal/scanctl"
)

func cursorFixture(t *testing.T) (string, string, *atomic.Uint64) {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	oldID, oldWriters := currentEventID, listWriters
	t.Cleanup(func() { currentEventID, listWriters = oldID, oldWriters })
	id := &atomic.Uint64{}
	currentEventID = id.Load
	listWriters = func(context.Context) ([]string, bool, error) { return nil, true, nil }
	fakeReplay(t, events())
	return home, filepath.Join(t.TempDir(), storeFileName), id
}

func cursorStore(t *testing.T, home, file string, runID uint64) *SizeStore {
	t.Helper()
	id, ok := identify(home)
	if !ok {
		t.Fatal("cannot identify fixture root")
	}
	s := &SizeStore{
		file: file, home: home, dev: id.dev, volume: "test-volume", boot: "test-boot", runID: runID,
		loaded: map[string]*storedSize{}, valid: map[*storedSize]bool{}, fresh: map[string]*storedSize{},
		ready: make(chan struct{}), cancel: func() {}, patience: time.Now().Add(lookupPatience),
	}
	s.load(context.Background(), time.Now())
	if len(s.entries) == 0 && s.newest <= s.runID {
		s.state = replayOK
		close(s.ready)
	} else {
		s.validate(context.Background())
	}
	return s
}

func savedCursors(t *testing.T, file string) map[string]storedSize {
	t.Helper()
	f, ok := decodeStore(mustRead(t, file))
	if !ok {
		t.Fatal("invalid saved cache")
	}
	entries := make(map[string]storedSize, len(f.Entries))
	for _, e := range f.Entries {
		entries[e.Path] = e
	}
	return entries
}

func TestStoreLongScanKeepsRecentWalks(t *testing.T) {
	for _, gap := range []uint64{replayMaxWindow, replayMaxWindow + 1} {
		t.Run(fmt.Sprint(gap), func(t *testing.T) {
			home, file, id := cursorFixture(t)
			early, late := filepath.Join(home, "early"), filepath.Join(home, "late")
			tree(t, early, 40, 5000)
			tree(t, late, 40, 5000)
			s := cursorStore(t, home, file, 100)
			ctx := WithSizeStore(context.Background(), s)
			id.Store(100)
			if _, err := Size(ctx, early, nil); err != nil {
				t.Fatal(err)
			}
			id.Store(100 + gap)
			want, err := Size(ctx, late, nil)
			if err != nil {
				t.Fatal(err)
			}
			WaitCache(ctx)
			s.Close()
			if got := savedCursors(t, file)[late].EventID; got != 100+gap {
				t.Fatalf("late walk cursor = %d, want %d", got, 100+gap)
			}
			var replayed uint64
			fakeReplay(t, func(_ context.Context, _ []string, since uint64, _ time.Duration, _ func(fsevents.Event) bool) bool {
				replayed = since
				return true
			})
			s = cursorStore(t, home, file, 100+gap)
			ctx = WithSizeStore(context.Background(), s)
			before, _ := Walked()
			if _, err := Size(ctx, early, nil); err != nil {
				t.Fatal(err)
			}
			got, err := Size(ctx, late, nil)
			if err != nil || got != want {
				t.Fatalf("late result = %+v, %v; want %+v", got, err, want)
			}
			WaitCache(ctx)
			s.Close()
			after, _ := Walked()
			wantHits, wantWalks, wantSince := int64(2), int64(0), uint64(100)
			if gap > replayMaxWindow {
				wantHits, wantWalks, wantSince = 1, 1, 100+gap
			}
			if s.hits.Load() != wantHits || after-before != wantWalks || replayed != wantSince {
				t.Fatalf("hits=%d walks=%d since=%d; want %d, %d, %d", s.hits.Load(), after-before, replayed, wantHits, wantWalks, wantSince)
			}
		})
	}
}

func TestStoreWalkCursorPrecedesRootIdentity(t *testing.T) {
	home, file, _ := cursorFixture(t)
	root := filepath.Join(home, "root")
	tree(t, root, 40, 1000)
	s := cursorStore(t, home, file, 100)
	currentEventID = func() uint64 {
		if err := os.Rename(root, root+"-old"); err != nil {
			t.Fatal(err)
		}
		tree(t, root, 40, 5000)
		return 200
	}
	c := &sizeCache{store: s}
	want, err := c.measure(scanctl.Ensure(context.Background()), root)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s = cursorStore(t, home, file, 300)
	defer s.Close()
	if got, ok := s.lookup(context.Background(), root); !ok || got != want {
		t.Fatalf("root identified before cursor capture: hit=%v stats=%+v", ok, got)
	}
}

func TestStoreMutationDuringWalkIsReplayed(t *testing.T) {
	home, file, id := cursorFixture(t)
	root := filepath.Join(home, "root")
	tree(t, root, 40, 5000)
	id.Store(200)
	s := cursorStore(t, home, file, 100)
	start, ok := s.beginWalk(context.Background(), root)
	if !ok {
		t.Fatal("walk could not start")
	}
	entered, resume := make(chan struct{}), make(chan struct{})
	done := make(chan struct{})
	var st Stats
	var err error
	go func() {
		defer close(done)
		st, err = Size(context.Background(), root, &Options{Skip: func(path, _ string) bool {
			if path == filepath.Join(root, "a") {
				close(entered)
				<-resume
			}
			return false
		}})
	}()
	<-entered
	changed := filepath.Join(root, "a", "b", "f0")
	grow(t, changed, 20_000)
	id.Store(300)
	close(resume)
	<-done
	if err != nil {
		t.Fatal(err)
	}
	s.record(root, start, st, time.Second)
	s.Close()
	if got := savedCursors(t, file)[root].EventID; got != 200 {
		t.Fatalf("walk cursor = %d, want the pre-mutation cursor 200", got)
	}
	fakeReplay(t, func(_ context.Context, _ []string, since uint64, _ time.Duration, cb func(fsevents.Event) bool) bool {
		if since < 300 {
			cb(fsevents.Event{Path: changed, ID: 300, Flags: fsevents.FlagItemModified})
		}
		return true
	})
	s = cursorStore(t, home, file, 400)
	defer s.Close()
	if _, ok := s.lookup(context.Background(), root); ok {
		t.Fatal("mutation during the walk was skipped by replay")
	}
}

func TestStoreUnavailableWalkCursorStillMeasures(t *testing.T) {
	for _, cursor := range []uint64{0, 99, 100} {
		t.Run(fmt.Sprint(cursor), func(t *testing.T) {
			home, file, id := cursorFixture(t)
			root := filepath.Join(home, "root")
			tree(t, root, 40, 1000)
			id.Store(cursor)
			s := cursorStore(t, home, file, 100)
			ctx := WithSizeStore(context.Background(), s)
			st, err := Size(ctx, root, nil)
			WaitCache(ctx)
			s.Close()
			if err != nil || st.Files != 40 {
				t.Fatalf("walk skipped: stats=%+v err=%v", st, err)
			}
			_, saved := savedCursors(t, file)[root]
			if saved != (cursor == 100) {
				t.Fatalf("cursor %d: saved=%v", cursor, saved)
			}
		})
	}
}

func TestStoreCancelledCursorCaptureDoesNotPersist(t *testing.T) {
	home, file, _ := cursorFixture(t)
	root := filepath.Join(home, "root")
	tree(t, root, 40, 1000)
	s := cursorStore(t, home, file, 100)
	ctx, cancel := context.WithCancel(context.Background())
	currentEventID = func() uint64 { cancel(); return 200 }
	ctx = WithSizeStore(ctx, s)
	_, err := Size(ctx, root, nil)
	WaitCache(ctx)
	s.Close()
	if err == nil || len(savedCursors(t, file)) != 0 {
		t.Fatalf("cancelled walk persisted: err=%v", err)
	}
}

func sharedEntry(path string, shared bool, cursor uint64) storedSize {
	return storedSize{Path: path, Dir: filepath.Dir(path), Real: path, Shared: shared, EventID: cursor, Measured: time.Now().UnixNano()}
}

func TestStoreLoadingIncompleteSharedCohort(t *testing.T) {
	for _, loss := range []string{"window", "expired", "identity", "zero cursor", "duplicate"} {
		t.Run(loss, func(t *testing.T) {
			home, file, _ := cursorFixture(t)
			runID := uint64(replayMaxWindow + 100)
			a, b, plain := sharedEntry(home+"/a", true, runID), sharedEntry(home+"/b", true, runID), sharedEntry(home+"/plain", false, runID)
			entries := []storedSize{a, b, plain}
			switch loss {
			case "window":
				entries[0].EventID = 1
			case "expired":
				entries[0].Measured = time.Now().Add(-25 * time.Hour).UnixNano()
			case "identity":
				entries[0].Real = ""
			case "zero cursor":
				entries[0].EventID = 0
			case "duplicate":
				entries = append(entries, a)
			}
			if err := writeStore(file, storeFile{Version: storeVersion, Volume: "test-volume", Boot: "test-boot", Entries: entries}); err != nil {
				t.Fatal(err)
			}
			s := cursorStore(t, home, file, runID)
			if s.loaded[a.Path] != nil || s.loaded[b.Path] != nil || s.loaded[plain.Path] == nil || s.state != replayOK {
				t.Fatalf("lost shared member left candidates: %+v, state=%s", s.loaded, stateName(s.state))
			}
			s.Close()
			if out := savedCursors(t, file); len(out) != 1 || out[plain.Path].Path != plain.Path {
				t.Fatalf("saved incomplete shared cohort: %+v", out)
			}
		})
	}
}

func TestStoreCloseRequiresSharedCoverage(t *testing.T) {
	for _, loss := range []string{"none", "shared replacement", "invalidated", "expired", "nonshared replacement", "canonical real", "canonical dir", "fresh expired", "fresh eviction"} {
		t.Run(loss, func(t *testing.T) {
			home, file, _ := cursorFixture(t)
			a, b, plain := sharedEntry(home+"/a", true, 100), sharedEntry(home+"/b", true, 100), sharedEntry(home+"/plain", false, 100)
			if err := writeStore(file, storeFile{Version: storeVersion, Volume: "test-volume", Boot: "test-boot", Entries: []storedSize{a, b, plain}}); err != nil {
				t.Fatal(err)
			}
			s := cursorStore(t, home, file, 200)
			switch loss {
			case "invalidated":
				delete(s.valid, s.loaded[a.Path])
			case "expired":
				s.loaded[a.Path].Measured = time.Now().Add(-25 * time.Hour).UnixNano()
			case "shared replacement", "nonshared replacement", "canonical real", "canonical dir":
				id := rootID{dev: s.dev, isDir: true, dir: a.Dir, real: a.Real}
				st := Stats{Bytes: 100, Reclaim: 50, Files: 40}
				if loss == "nonshared replacement" {
					st.Reclaim = st.Bytes
				} else if loss == "canonical real" {
					id.real += "-new"
				} else if loss == "canonical dir" {
					id.dir += "-new"
				}
				s.record(a.Path, walkStart{root: id, since: 250}, st, time.Second)
			case "fresh expired", "fresh eviction":
				path := home + "/fresh-shared"
				id := rootID{dev: s.dev, isDir: true, dir: home, real: path}
				s.record(path, walkStart{root: id, since: 250}, Stats{Bytes: 100, Reclaim: 50, Files: 40}, time.Second)
				if s.fresh[path].EventID != 200 {
					t.Fatal("shared walk did not keep the run cursor")
				}
				if loss == "fresh expired" {
					s.fresh[path].Measured = time.Now().Add(-25 * time.Hour).UnixNano()
					break
				}
				s.fresh[path].Measured = time.Now().Add(-time.Hour).UnixNano()
				for i := range storeMaxEntries - 3 {
					p := fmt.Sprintf("%s/filler-%d", home, i)
					e := sharedEntry(p, false, 250)
					s.fresh[p] = &e
				}
			}
			s.Close()
			out := savedCursors(t, file)
			shared := 0
			for _, e := range out {
				if e.Shared {
					shared++
				}
			}
			wantShared := 0
			if loss == "none" || loss == "shared replacement" {
				wantShared = 2
			}
			if shared != wantShared || out[plain.Path].Path != plain.Path {
				t.Fatalf("shared=%d want=%d; plain retained=%v", shared, wantShared, out[plain.Path].Path == plain.Path)
			}
			if loss == "fresh eviction" && len(out) != storeMaxEntries-2 {
				t.Fatalf("capacity refilled after shared loss: saved=%d", len(out))
			}
		})
	}
}

func TestStoreDiscardedSharedFutureStillFails(t *testing.T) {
	home := storeHome(t)
	oldWriters := listWriters
	t.Cleanup(func() { listWriters = oldWriters })
	listWriters = func(context.Context) ([]string, bool, error) { return nil, true, nil }
	fakeReplay(t, events())
	s := openRun(t)
	a, b := sharedEntry(home+"/future", true, s.runID+1_000_000), sharedEntry(home+"/expired", true, s.runID)
	b.Measured = time.Now().Add(-25 * time.Hour).UnixNano()
	f := storeFile{Version: storeVersion, Volume: s.volume, Boot: s.boot, Entries: []storedSize{a, b}}
	s.Close()
	if err := writeStore(SizeCachePath(), f); err != nil {
		t.Fatal(err)
	}
	s = openRun(t)
	defer s.Close()
	if s.state != replayFailed || len(s.valid) != 0 {
		t.Fatalf("discarding the cohort hid the future cursor: state=%s valid=%d", stateName(s.state), len(s.valid))
	}
}
