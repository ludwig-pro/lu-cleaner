package fsx

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/gob"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/ludwig-pro/lu-cleaner/internal/fsevents"
	"github.com/ludwig-pro/lu-cleaner/internal/scanctl"
	"golang.org/x/sys/unix"
)

// ---------------------------------------------------------------- persistent size cache
//
// A scan measures millions of files, most of which did not change since the
// previous scan. SizeStore keeps, per measured root, the Stats of the last
// complete walk with the identity of the root and the FSEvents event id taken
// before the walk, in ~/Library/Caches/lu-cleaner/sizes.v1. The next run
// replays the FSEvents history since then (fsevents.Changes: file-level
// events, up to the moment of the call) and drops every entry that may have
// changed; the others answer Size(ctx, path, nil) without walking. Items are
// still discovered live by the providers: only sizes come from the cache.
//
// An event on path P invalidates the cached root R when:
//   - P equals R or lies below it (R's content changed);
//   - P is an ancestor of R: that directory itself was created, removed,
//     renamed or had its metadata changed, so R may have been moved away,
//     modified and moved back;
//   - P carries FlagMustScanSubDirs and contains R or lies below it.
//
// Writes to a file that stays open report nothing until it is closed (only
// the close, or an ftruncate, is an event): a VM disk, a database and its
// -wal file, a log. Every run lists the files open for writing by the
// processes it may inspect (openForWriting, like lsof) and drops the entries
// holding one. Still unseen: files written by root daemons (not listed), a
// file of the tree written through a hard link outside it while open (listed
// by that other path), writes through a shared memory mapping whose file was
// closed, and holes punched in a file (F_PUNCHHOLE reports nothing, even on
// close).
//
// What deleting a tree frees (Stats.Reclaim) also depends on the other hard
// links and clones of its data, outside it. Creating a clone or a hard link
// reports an event inside the tree (on the source, or on its directory), and
// so does a write through a hard link made outside; but removing a hard link
// or a clone outside, or modifying a clone outside, reports nothing inside
// it. So a change inside a tree sharing data with others (Reclaim < Bytes: a
// node_modules installed from the bun cache or the pnpm store, the store
// itself) invalidates every such "shared" tree (after deleting a
// node_modules, the store frees more), and shared trees expire after
// sharedMaxAge. Not seen until the tree changes or its entry expires: copies
// and links removed or modified outside the cached trees, which make Reclaim
// look smaller than what deleting would free. (Hard link events cannot be
// used: git and Spotlight create some all the time.)
//
// Nothing is trusted when the history cannot be: events dropped, ids wrapped
// or going backwards, another FSEvents database (volume UUID), a replay that
// times out or would be too long (the replay time grows with the activity of
// the whole machine since the oldest entry, see replayMaxWindow), files open
// for writing that cannot be listed, a damaged cache file (checksum). The
// history only knows what happened while this system was running: entries
// measured before the current boot are dropped (changes made from Recovery,
// another system or target disk mode; the last events before a crash, which
// fseventsd may not have written yet), and the store is disabled when the home
// directory is not on the data volume of the running system (an external
// disk comes back with the same FSEvents UUID after changes made elsewhere,
// or here while it was mounted with its ownership ignored: fseventsd does
// not record anything then). A hit also re-checks the root itself: same
// inode on the home volume, same mtime and ctime (a rename updates the
// ctime), same resolved path.
//
// Only complete walks are stored: not cancelled, no unreadable entry (they
// depend on permissions that change without events), root on the home
// volume. Roots outside the home directory are replayed from their parent
// directory only: a rename of a directory above it is not seen (moving
// /opt/homebrew away, changing it and moving it back).
//
// LU_NO_CACHE=1 disables the store (the per-run memo of WithCache stays).
// Test binaries never use the real cache directory: the store is disabled in
// tests unless SizeCacheDir is set.

// listWriters lists the files open for writing and replayChanges replays
// the FSEvents history (variables for tests).
var (
	listWriters   = openForWriting
	replayChanges = fsevents.Changes
)

// SizeCacheDir overrides the directory of the persistent size cache (tests).
// Empty means $XDG_CACHE_HOME/lu-cleaner when XDG_CACHE_HOME is set, else
// ~/Library/Caches/lu-cleaner.
var SizeCacheDir string

const (
	storeFileName   = "sizes.v1"
	storeVersion    = 2 // 2: checksum, boot session
	storeMagic      = "lu-sizes"
	storeMaxAge     = 7 * 24 * time.Hour
	sharedMaxAge    = 24 * time.Hour
	storeMaxEntries = 50_000
	storeMaxBytes   = 64 << 20
	// Trees smaller than storeMinEntries whose walk took less than
	// storeMinWalk are cheaper to walk again than to validate and store.
	storeMinEntries = 32
	storeMinWalk    = 10 * time.Millisecond
	// replayMaxWindow bounds the replay: fseventsd reads its whole log since
	// the oldest entry (about 1-2 million ids per second), so a busy machine
	// days after the last scan would spend longer replaying than walking.
	replayMaxWindow = 16_000_000
	replayTimeout   = 8 * time.Second
	syncTimeout     = time.Second
	// lookupPatience: how long after the store was opened Size calls wait for
	// the replay; past it they walk (the replay goes on for the later calls).
	lookupPatience = 3 * time.Second
)

// storedSize is one entry of the cache file.
type storedSize struct {
	Path     string // as requested (lookup key)
	Dir      string // canonical parent directory (symlinks resolved, on-disk case)
	Real     string // FoldPath(Dir/on-disk name): matched against events
	Stats    Stats
	Shared   bool // shares data with files outside it (Reclaim < Bytes)
	Ino      uint64
	Mtime    int64 // root mtime, ns
	Ctime    int64 // root ctime, ns
	EventID  uint64
	Measured int64 // unix ns
}

type storeFile struct {
	Version int
	Volume  string // FSEvents UUID of the home volume
	Boot    string // boot session (kern.bootsessionuuid) of the run that saved it
	Entries []storedSize
}

type replayState int

const (
	replayPending   replayState = iota
	replayOK                    // every loaded entry still valid is trusted
	replayCancelled             // run cancelled: loaded entries kept untouched
	replayFailed                // history unusable: loaded entries dropped
)

// SizeStore is the persistent size cache of one engine run. A nil
// *SizeStore is valid and disabled.
type SizeStore struct {
	file   string
	home   string // canonical home directory
	dev    int32  // device of the home volume
	volume string
	boot   string             // boot session
	runID  uint64             // FSEvents id taken when the store was opened, before any walk
	cancel context.CancelFunc // stops the replay (Close)

	loaded   map[string]*storedSize // from disk (immutable after open)
	entries  []*storedSize          // same, in file order
	ready    chan struct{}          // closed once the replay decided
	since    uint64                 // oldest event id of the entries
	newest   uint64                 // newest one
	patience time.Time              // lookups stop waiting for the replay then

	mu     sync.Mutex
	valid  map[*storedSize]bool
	fresh  map[string]*storedSize // measured during this run
	state  replayState
	events int
	replay time.Duration
	reason string
	// what the replay invalidated (tracing)
	invalidations string

	hits, hitFiles, misses, impatient atomic.Int64
	walks0, files0                    int64
	skipOtherVolume                   skipCount // walks not stored, by reason (tracing)
	skipErrors, skipSmall             skipCount
}

type skipCount struct{ trees, files atomic.Int64 }

// walk counters (every walk, cached or not: tracing and tests)
var walkCount, walkFiles atomic.Int64

// Walked returns how many trees Size walked in this process and how many
// entries they held (cache hits are not walks).
func Walked() (trees, files int64) { return walkCount.Load(), walkFiles.Load() }

// Tracing reports whether LU_TRACE is set.
func Tracing() bool { return traceOn }

// SizeCachePath returns the file of the persistent size cache.
func SizeCachePath() string {
	dir := SizeCacheDir
	if dir == "" {
		if x := os.Getenv("XDG_CACHE_HOME"); x != "" && filepath.IsAbs(x) {
			dir = filepath.Join(x, "lu-cleaner")
		} else if home, err := os.UserHomeDir(); err == nil && home != "" {
			dir = filepath.Join(home, "Library", "Caches", "lu-cleaner")
		}
	}
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, storeFileName)
}

func storeDisabled() bool {
	if v := os.Getenv("LU_NO_CACHE"); v != "" && v != "0" {
		return true
	}
	// never read or write the user's cache from a test binary
	return testing.Testing() && SizeCacheDir == ""
}

// OpenSizeStore loads the persistent size cache and starts replaying the
// FSEvents history since its entries were measured (in the background: Size
// calls for cached paths wait for it). It returns nil when the cache is
// disabled (LU_NO_CACHE=1, FSEvents unavailable, no home directory, or a
// home directory that is not on the data volume of the running system). ctx
// bounds the replay. Call Close at the end of the run.
func OpenSizeStore(ctx context.Context) *SizeStore {
	ctx = scanctl.Ensure(ctx)
	if ctx.Err() != nil || storeDisabled() || !fsevents.Supported() {
		return nil
	}
	file := SizeCachePath()
	homeEnv, err := os.UserHomeDir()
	if file == "" || err != nil || homeEnv == "" {
		return nil
	}
	home, ok := canonicalDirContext(ctx, homeEnv)
	if !ok || !onBootDataVolumeContext(ctx, home) {
		// The history of another volume misses what changed while it was
		// mounted elsewhere, or here with its ownership ignored (fseventsd
		// does not log then): an external disk can come back with the same
		// FSEvents UUID and changes the history never saw.
		return nil
	}
	boot := bootSessionContext(ctx)
	var hst unix.Stat_t
	if boot == "" || scanctl.DoIO(ctx, func() error { return unix.Stat(home, &hst) }) != nil {
		return nil
	}
	// The id comes before any walk: every change after it is replayed next
	// time. Sync makes it include the changes made just before the run
	// (fseventsd records them with a small delay), which would otherwise
	// invalidate the trees measured now at the next run.
	runID, _ := fsevents.Sync(ctx, syncTimeout)
	var volume string
	if err := scanctl.DoIO(ctx, func() error {
		volume = fsevents.VolumeUUID(home)
		return nil
	}); err != nil || ctx.Err() != nil {
		return nil
	}
	s := &SizeStore{
		file: file, home: home, dev: hst.Dev,
		boot:   boot,
		runID:  runID,
		volume: volume,
		loaded: map[string]*storedSize{},
		valid:  map[*storedSize]bool{},
		fresh:  map[string]*storedSize{},
		ready:  make(chan struct{}),
		// lookups wait for the replay until then
		patience: time.Now().Add(lookupPatience),
	}
	if s.runID == 0 || s.volume == "" {
		return nil
	}
	s.walks0, s.files0 = Walked()
	s.load(ctx, time.Now())
	if ctx.Err() != nil {
		return nil
	}
	if len(s.entries) == 0 {
		s.state = replayOK
		s.cancel = func() {}
		close(s.ready)
		return s
	}
	vctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	go s.validate(vctx)
	return s
}

// onBootDataVolume reports whether p lies on the data volume of the running
// system, which only this system mounts (and always while it runs).
func onBootDataVolume(p string) bool {
	return onBootDataVolumeContext(context.Background(), p)
}

func onBootDataVolumeContext(ctx context.Context, p string) bool {
	var fs unix.Statfs_t
	return scanctl.DoIO(ctx, func() error { return unix.Statfs(p, &fs) }) == nil && ctx.Err() == nil && unix.ByteSliceToString(fs.Mntonname[:]) == "/System/Volumes/Data"
}

// bootSession identifies the current boot. Entries measured before it are
// not trusted: changes made while this system was not running (from
// Recovery, another system, target disk mode) are not in its history, nor
// the events a crash may have lost (fseventsd writes its log lazily).
func bootSession() string {
	return bootSessionContext(context.Background())
}

func bootSessionContext(ctx context.Context) string {
	var b string
	err := scanctl.DoIO(ctx, func() error {
		var err error
		b, err = unix.Sysctl("kern.bootsessionuuid")
		return err
	})
	if err != nil || ctx.Err() != nil {
		return ""
	}
	return b
}

// expired reports whether an entry is too old to be trusted at now.
func (e *storedSize) expired(now time.Time) bool {
	age := storeMaxAge
	if e.Shared {
		age = sharedMaxAge
	}
	return e.Measured < now.Add(-age).UnixNano()
}

// load reads the cache file; anything unexpected starts from scratch.
func (s *SizeStore) load(ctx context.Context, now time.Time) {
	var fi os.FileInfo
	err := scanctl.DoIO(ctx, func() error {
		var err error
		fi, err = os.Stat(s.file)
		return err
	})
	if err != nil || fi.Size() > storeMaxBytes {
		return
	}
	var data []byte
	err = scanctl.DoIO(ctx, func() error {
		var err error
		data, err = os.ReadFile(s.file)
		return err
	})
	if err != nil || ctx.Err() != nil {
		return
	}
	f, ok := decodeStore(data)
	if !ok || f.Version != storeVersion || f.Volume != s.volume || f.Boot != s.boot {
		return
	}
	for i := range f.Entries {
		if ctx.Err() != nil {
			return
		}
		e := &f.Entries[i]
		if e.expired(now) || e.EventID == 0 || e.Path == "" || e.Real == "" || e.Dir == "" || s.loaded[e.Path] != nil {
			continue
		}
		if e.EventID < s.runID && s.runID-e.EventID > replayMaxWindow {
			continue // too old to replay (see replayMaxWindow): would fail the others
		}
		s.loaded[e.Path] = e
		s.entries = append(s.entries, e)
		if s.since == 0 || e.EventID < s.since {
			s.since = e.EventID
		}
		s.newest = max(s.newest, e.EventID)
	}
}

// validate replays the history since the oldest entry and keeps the entries
// no event touched.
func (s *SizeStore) validate(ctx context.Context) {
	start := time.Now()
	state, reason := replayOK, ""
	ix, _ := newStoreIndexContext(ctx, s.entries)
	// Files open for writing change without events until they are closed
	// (the close is reported): listed after runID, they cover every write
	// the replay cannot see.
	var writers []string
	writersOK := false
	var writerErr error
	if ctx.Err() == nil {
		writers, writersOK, writerErr = listWriters(ctx)
	}
	switch {
	case ctx.Err() != nil:
		state, reason = replayCancelled, "cancelled"
	case !writersOK || writerErr != nil:
		state, reason = replayFailed, "cannot list the files open for writing"
	case s.newest > s.runID: // any of them: the ids are not comparable
		state, reason = replayFailed, "event ids went backwards"
	case s.runID-s.since > replayMaxWindow:
		state, reason = replayFailed, fmt.Sprintf("history too long to replay (%d event ids)", s.runID-s.since)
	default:
		rescan := ""
		ok := replayChanges(ctx, s.watchRoots(), s.since, replayTimeout, func(e fsevents.Event) bool {
			if ctx.Err() != nil {
				return false
			}
			if e.Flags&fsevents.FlagsRescanAll != 0 {
				rescan = fmt.Sprintf("events were dropped: %s flags %#x", e.Path, e.Flags)
				return false
			}
			ix.apply(e)
			return true
		})
		for _, w := range writers {
			if ctx.Err() != nil {
				break
			}
			ix.applyWriter(w)
		}
		if ctx.Err() == nil {
			ix.finish()
		}
		switch {
		case ctx.Err() != nil:
			state, reason = replayCancelled, "cancelled"
		case !ok:
			state, reason = replayFailed, "history unavailable or replay timed out"
		case rescan != "":
			state, reason = replayFailed, rescan
		}
	}
	valid := map[*storedSize]bool{}
	if state == replayOK {
		for _, e := range s.entries {
			if ctx.Err() != nil {
				break
			}
			if !ix.invalid[e] {
				valid[e] = true
			}
		}
	}
	s.mu.Lock()
	if ctx.Err() != nil {
		state, reason = replayCancelled, "cancelled"
	}
	s.state, s.reason, s.replay, s.events = state, reason, time.Since(start), ix.events
	s.invalidations = ix.summary()
	if state == replayOK {
		s.valid = valid
	}
	s.mu.Unlock()
	if traceOn {
		fmt.Fprintf(os.Stderr, "[trace] cache validation %s: entries=%d valid=%d events=%d took=%s%s\n",
			stateName(s.state), len(s.entries), len(s.valid), s.events, s.replay.Round(time.Millisecond), reasonSuffix(s.reason))
	}
	close(s.ready)
}

// watchRoots returns the roots to replay: the home directory, plus the
// parent of every entry outside it.
func (s *SizeStore) watchRoots() []string {
	roots := []string{s.home}
	seen := map[string]bool{s.home: true}
	for _, e := range s.entries {
		if Within(e.Dir, s.home) || seen[e.Dir] {
			continue
		}
		seen[e.Dir] = true
		roots = append(roots, e.Dir)
	}
	return roots
}

// lookup answers from the cache when the entry for path is still valid.
func (s *SizeStore) lookup(ctx context.Context, path string) (Stats, bool) {
	if s == nil || ctx.Err() != nil {
		return Stats{}, false
	}
	e := s.loaded[path]
	if e == nil {
		s.misses.Add(1)
		return Stats{}, false
	}
	select {
	case <-s.ready:
	default:
		t := time.NewTimer(time.Until(s.patience))
		select {
		case <-s.ready:
			t.Stop()
		case <-t.C:
			s.impatient.Add(1)
			s.misses.Add(1)
			return Stats{}, false
		case <-ctx.Done():
			t.Stop()
			return Stats{}, false
		}
	}
	if ctx.Err() != nil {
		return Stats{}, false
	}
	s.mu.Lock()
	ok := s.valid[e]
	s.mu.Unlock()
	if ok {
		id, idOK := identifyContext(ctx, path)
		if ctx.Err() != nil {
			return Stats{}, false
		}
		ok = idOK && id.dev == s.dev && id.ino == e.Ino && id.mtime == e.Mtime && id.ctime == e.Ctime && id.real == e.Real
		if !ok {
			s.mu.Lock()
			delete(s.valid, e)
			s.mu.Unlock()
		}
	}
	if !ok {
		s.misses.Add(1)
		return Stats{}, false
	}
	if ctx.Err() != nil {
		return Stats{}, false
	}
	s.hits.Add(1)
	s.hitFiles.Add(e.Stats.Files)
	return e.Stats, true
}

// record stores a complete walk of path whose root was identified (before
// the walk) as id; took is how long the walk took.
func (s *SizeStore) record(path string, id rootID, st Stats, took time.Duration) {
	if s == nil {
		return
	}
	var skip *skipCount
	switch {
	case id.dev != s.dev:
		skip = &s.skipOtherVolume
	case !id.isDir:
		// a single file: a stat is cheaper than validating an entry
		skip = &s.skipSmall
	case st.Errors != 0:
		skip = &s.skipErrors
	case st.Files+st.Dirs < storeMinEntries && took < storeMinWalk:
		skip = &s.skipSmall
	}
	if skip != nil {
		skip.trees.Add(1)
		skip.files.Add(st.Files)
		return
	}
	e := &storedSize{
		Path: path, Dir: id.dir, Real: id.real, Stats: st,
		Shared: st.Reclaim < st.Bytes,
		Ino:    id.ino, Mtime: id.mtime, Ctime: id.ctime,
		EventID: s.runID, Measured: time.Now().UnixNano(),
	}
	s.mu.Lock()
	s.fresh[path] = e
	s.mu.Unlock()
}

// Close saves the cache (valid entries and this run's walks) and prints a
// summary when LU_TRACE is set. Nothing may use the store afterwards.
func (s *SizeStore) Close() {
	if s == nil {
		return
	}
	// A replay still running is not needed any more (this run is over):
	// stop it, the loaded entries are then kept as they were.
	s.cancel()
	<-s.ready
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	out := make([]storedSize, 0, len(s.entries)+len(s.fresh))
	for _, e := range s.fresh {
		out = append(out, *e)
	}
	kept := 0
	for _, e := range s.entries {
		if s.fresh[e.Path] != nil || e.expired(now) {
			continue
		}
		switch s.state {
		case replayOK:
			if !s.valid[e] {
				continue
			}
			c := *e
			c.EventID = s.runID // validated up to now
			out = append(out, c)
		case replayCancelled:
			out = append(out, *e) // nothing learned: keep as it was
		default:
			continue
		}
		kept++
	}
	if len(out) > storeMaxEntries {
		sort.Slice(out, func(i, j int) bool { return out[i].Measured > out[j].Measured })
		out = out[:storeMaxEntries]
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	err := writeStore(s.file, storeFile{Version: storeVersion, Volume: s.volume, Boot: s.boot, Entries: out})
	if traceOn {
		trees, files := Walked()
		msg := fmt.Sprintf("hits %d (%d files not walked), misses %d (%d before the replay ended); walked %d trees, %d files; replay %s: %d events in %v%s; saved %d entries (%d kept, %d new)",
			s.hits.Load(), s.hitFiles.Load(), s.misses.Load(), s.impatient.Load(), trees-s.walks0, files-s.files0,
			stateName(s.state), s.events, s.replay.Round(time.Millisecond), reasonSuffix(s.reason), len(out), kept, len(s.fresh))
		if s.invalidations != "" {
			msg += "; " + s.invalidations
		}
		for _, c := range []struct {
			name string
			n    *skipCount
		}{{"unreadable entries", &s.skipErrors}, {"other volume", &s.skipOtherVolume}, {"small", &s.skipSmall}} {
			if n := c.n.trees.Load(); n > 0 {
				msg += fmt.Sprintf("; not stored (%s): %d trees, %d files", c.name, n, c.n.files.Load())
			}
		}
		if err != nil {
			msg += "; save failed: " + err.Error()
		}
		fmt.Fprintf(os.Stderr, "[trace] cache %s\n", msg)
	}
}

func stateName(st replayState) string {
	switch st {
	case replayOK:
		return "ok"
	case replayCancelled:
		return "cancelled"
	case replayFailed:
		return "failed"
	}
	return "pending"
}

func reasonSuffix(r string) string {
	if r == "" {
		return ""
	}
	return " (" + r + ")"
}

// The cache file is storeMagic, the CRC-32 (Castagnoli) of the rest, then
// the gob encoding of a storeFile: a damaged file (a flipped bit in an event
// id would skip changes) is ignored as a whole.
var storeCRC = crc32.MakeTable(crc32.Castagnoli)

func decodeStore(data []byte) (storeFile, bool) {
	var f storeFile
	n := len(storeMagic)
	if len(data) < n+4 || string(data[:n]) != storeMagic {
		return f, false
	}
	payload := data[n+4:]
	if crc32.Checksum(payload, storeCRC) != binary.LittleEndian.Uint32(data[n:]) {
		return f, false
	}
	if gob.NewDecoder(bytes.NewReader(payload)).Decode(&f) != nil {
		return storeFile{}, false
	}
	return f, true
}

// writeStore writes the cache file atomically (private to the user, at most
// storeMaxBytes). Close still saves after cancellation so unchanged entries
// keep their history. This maintenance has no scan context and is confined
// to the cache directory and its own .sizes-* temporary files.
func writeStore(file string, f storeFile) error {
	var buf bytes.Buffer
	buf.WriteString(storeMagic)
	buf.Write(make([]byte, 4)) // checksum, below
	if err := gob.NewEncoder(&buf).Encode(f); err != nil {
		return err
	}
	b := buf.Bytes()
	binary.LittleEndian.PutUint32(b[len(storeMagic):], crc32.Checksum(b[len(storeMagic)+4:], storeCRC))
	if buf.Len() > storeMaxBytes {
		return fmt.Errorf("cache too big (%d bytes)", buf.Len())
	}
	dir := filepath.Dir(file)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// temporary files of runs killed while saving
	if old, _ := filepath.Glob(filepath.Join(dir, ".sizes-*")); len(old) > 0 {
		for _, p := range old {
			if fi, err := os.Lstat(p); err == nil && fi.Mode().IsRegular() && time.Since(fi.ModTime()) > time.Hour {
				os.Remove(p)
			}
		}
	}
	tmp, err := os.CreateTemp(dir, ".sizes-*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(buf.Bytes())
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), file)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

// ---------------------------------------------------------------- root identity

// rootID identifies a measured root: its inode and times, and its resolved
// path (canonical parent directory + name).
type rootID struct {
	dev          int32
	ino          uint64
	mtime, ctime int64
	isDir        bool
	dir, real    string
}

func identify(path string) (rootID, bool) {
	return identifyContext(context.Background(), path)
}

func identifyContext(ctx context.Context, path string) (rootID, bool) {
	var st unix.Stat_t
	if scanctl.DoIO(ctx, func() error { return unix.Lstat(path, &st) }) != nil || ctx.Err() != nil {
		return rootID{}, false
	}
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) || clean == "/" {
		return rootID{}, false
	}
	dir, ok := canonicalDirContext(ctx, filepath.Dir(clean))
	if !ok || Within(dir, "/System/Volumes") {
		// (the data volume is reported through its firmlinks, /Users…:
		// FSEvents would not match events spelled any other way)
		return rootID{}, false
	}
	id := rootID{
		dev: st.Dev, ino: st.Ino, mtime: st.Mtim.Nano(), ctime: st.Ctim.Nano(),
		isDir: st.Mode&unix.S_IFMT == unix.S_IFDIR, dir: dir,
	}
	name := filepath.Base(clean)
	if id.isDir {
		// The on-disk spelling of the root's own name, which events carry:
		// APFS folds names more than FoldPath does ("STRASSE" opens
		// "straße", "λογοσ" opens "λογος").
		if p, ok := canonicalPathContext(ctx, clean, unix.O_NOFOLLOW); ok && filepath.Dir(p) == dir {
			name = filepath.Base(p)
		}
	}
	id.real = FoldPath(filepath.Join(dir, name))
	return id, ctx.Err() == nil
}

// canonicalDir returns the path of directory p as the kernel knows it:
// symlinks resolved and the on-disk case of every component (FSEvents only
// reports events below a root spelled that way).
func canonicalDir(p string) (string, bool) { return canonicalPath(p, 0) }

func canonicalDirContext(ctx context.Context, p string) (string, bool) {
	return canonicalPathContext(ctx, p, 0)
}

// canonicalPath is canonicalDir with extra open flags (O_NOFOLLOW).
func canonicalPath(p string, flags int) (string, bool) {
	return canonicalPathContext(context.Background(), p, flags)
}

func canonicalPathContext(ctx context.Context, p string, flags int) (string, bool) {
	var buf [unix.PathMax]byte
	err := scanctl.DoIO(ctx, func() error {
		fd, err := unix.Open(p, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|flags, 0)
		if err != nil {
			return err
		}
		defer unix.Close(fd)
		if _, _, errno := unix.Syscall(unix.SYS_FCNTL, uintptr(fd), unix.F_GETPATH, uintptr(unsafe.Pointer(&buf[0]))); errno != 0 {
			return errno
		}
		return nil
	})
	if err != nil || ctx.Err() != nil {
		return "", false
	}
	n := bytes.IndexByte(buf[:], 0)
	if n <= 0 {
		return "", false
	}
	return string(buf[:n]), true
}

// ---------------------------------------------------------------- invalidation

// storeIndex matches events against the cached roots (folded paths).
type storeIndex struct {
	byReal    map[string][]*storedSize
	ancestors map[string]bool // strict ancestors of some root
	sorted    []*storedSize   // by Real
	invalid   map[*storedSize]bool
	events    int

	sharedChanged string // a tree sharing data with others that changed (first one)
	byContent     int    // entries invalidated by events on or above them
	byShared      int    // … because sharedChanged
	byWriters     int    // … because they hold a file open for writing
}

// summary describes the invalidations (tracing).
func (ix *storeIndex) summary() string {
	s := fmt.Sprintf("%d invalidated by changes", ix.byContent)
	if ix.byWriters > 0 {
		s += fmt.Sprintf(", %d holding files open for writing", ix.byWriters)
	}
	if ix.byShared > 0 {
		s += fmt.Sprintf(", %d sharing data with %s", ix.byShared, ix.sharedChanged)
	}
	return s
}

func newStoreIndex(entries []*storedSize) *storeIndex {
	ix, _ := newStoreIndexContext(context.Background(), entries)
	return ix
}

func newStoreIndexContext(ctx context.Context, entries []*storedSize) (*storeIndex, error) {
	ix := &storeIndex{byReal: map[string][]*storedSize{}, ancestors: map[string]bool{}, invalid: map[*storedSize]bool{}}
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return ix, err
		}
		ix.byReal[e.Real] = append(ix.byReal[e.Real], e)
		for q := parentOf(e.Real); q != ""; q = parentOf(q) {
			if ix.ancestors[q] {
				break // and all of its ancestors
			}
			ix.ancestors[q] = true
		}
		ix.sorted = append(ix.sorted, e)
	}
	sort.Slice(ix.sorted, func(i, j int) bool { return ix.sorted[i].Real < ix.sorted[j].Real })
	return ix, ctx.Err()
}

// parentOf returns the parent of a clean absolute path ("" for "/").
func parentOf(p string) string {
	if p == "/" || p == "" {
		return ""
	}
	i := strings.LastIndexByte(p, '/')
	if i <= 0 {
		return "/"
	}
	return p[:i]
}

// apply invalidates the roots an event may have changed.
func (ix *storeIndex) apply(e fsevents.Event) {
	if e.Path == "" || e.Path[0] != '/' {
		return
	}
	ix.events++
	p := FoldPath(filepath.Clean(e.Path))
	// p is a root or below one: that root's content changed
	for q := p; q != ""; q = parentOf(q) {
		for _, r := range ix.byReal[q] {
			ix.invalidate(r, &ix.byContent)
		}
	}
	// p is above some roots: the directory itself changed (created, removed,
	// renamed…), or everything below it must be rescanned
	if ix.ancestors[p] || e.Flags&fsevents.FlagMustScanSubDirs != 0 {
		ix.invalidateBelow(p)
	}
}

// applyWriter invalidates the roots holding path, a file open for writing.
func (ix *storeIndex) applyWriter(path string) {
	if path == "" || path[0] != '/' {
		return
	}
	for q := FoldPath(filepath.Clean(path)); q != ""; q = parentOf(q) {
		for _, r := range ix.byReal[q] {
			ix.invalidate(r, &ix.byWriters)
		}
	}
}

// invalidate marks r invalid, counting it in *count the first time.
func (ix *storeIndex) invalidate(r *storedSize, count *int) {
	if !ix.invalid[r] {
		ix.invalid[r] = true
		*count++
	}
	if r.Shared && ix.sharedChanged == "" {
		ix.sharedChanged = r.Path
	}
}

// invalidateBelow invalidates every root strictly below p.
func (ix *storeIndex) invalidateBelow(p string) {
	prefix := p + "/"
	if p == "/" {
		prefix = "/"
	}
	i := sort.Search(len(ix.sorted), func(i int) bool { return ix.sorted[i].Real >= prefix })
	for ; i < len(ix.sorted) && strings.HasPrefix(ix.sorted[i].Real, prefix); i++ {
		ix.invalidate(ix.sorted[i], &ix.byContent)
	}
}

// finish applies the rules that need the whole replay: what deleting a tree
// sharing data frees depends on the other copies.
func (ix *storeIndex) finish() {
	if ix.sharedChanged == "" {
		return
	}
	for _, r := range ix.sorted {
		if r.Shared && !ix.invalid[r] {
			ix.invalid[r] = true
			ix.byShared++
		}
	}
}
