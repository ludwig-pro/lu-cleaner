// Package fsevents replays the persistent macOS FSEvents history: which
// paths changed on a volume since a given event id, even while no process
// was watching (fseventsd records every change in /.fseventsd). The size
// cache (fsx) uses it to re-walk only the directories that changed since the
// previous scan.
//
// The real implementation needs cgo (CoreServices); without cgo, or on
// another OS, Supported is false and every replay reports ok=false, so
// callers fall back to a full walk.
//
// Cost: fseventsd reads its whole log since the requested id, whatever the
// watched roots (about 1-2 million event ids per second): the replay time
// grows with the activity of the machine since `since`, not with the number
// of changes below the roots.
package fsevents

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Event flags (FSEventStreamEventFlags, CoreServices/FSEvents.h).
const (
	FlagMustScanSubDirs    = 0x00000001 // changes below Path were coalesced: treat it as changed recursively
	FlagUserDropped        = 0x00000002
	FlagKernelDropped      = 0x00000004
	FlagEventIdsWrapped    = 0x00000008
	FlagHistoryDone        = 0x00000010 // marker: the history is replayed, later events are live ones
	FlagRootChanged        = 0x00000020
	FlagMount              = 0x00000040
	FlagUnmount            = 0x00000080
	FlagItemCreated        = 0x00000100
	FlagItemRemoved        = 0x00000200
	FlagItemInodeMetaMod   = 0x00000400
	FlagItemRenamed        = 0x00000800
	FlagItemModified       = 0x00001000
	FlagItemFinderInfoMod  = 0x00002000
	FlagItemChangeOwner    = 0x00004000
	FlagItemXattrMod       = 0x00008000
	FlagItemIsFile         = 0x00010000
	FlagItemIsDir          = 0x00020000
	FlagItemIsSymlink      = 0x00040000
	FlagItemIsHardlink     = 0x00100000
	FlagItemIsLastHardlink = 0x00200000
	FlagItemCloned         = 0x00400000

	// FlagsRescanAll are the flags after which the history can no longer
	// be trusted: events were lost, or ids are not comparable any more.
	FlagsRescanAll = FlagUserDropped | FlagKernelDropped | FlagEventIdsWrapped | FlagRootChanged
)

// Event is one change. Events are file-level: Path is the file or directory
// that was created, removed, renamed or modified (both the old and the new
// path of a rename are reported); the parent directory of a created or
// removed item is not reported separately.
type Event struct {
	Path  string
	Flags uint32
	ID    uint64
}

// DefaultTimeout bounds a replay when the caller gives no timeout: past it,
// the history is treated as unavailable (ok=false).
const DefaultTimeout = 3 * time.Second

// Supported reports whether this build can read the FSEvents history.
func Supported() bool { return supported }

// CurrentEventID returns the id of the most recent FSEvents event on this
// host (0 when unavailable). Take it BEFORE looking at the filesystem: a
// later Changes since that id then reports every change made after it.
func CurrentEventID() uint64 { return currentEventID() }

// VolumeUUID returns the UUID of the FSEvents database of the volume holding
// path ("" when unavailable). Event ids of a volume are only comparable
// while its UUID stays the same: it changes when the history is reset.
func VolumeUUID(path string) string { return volumeUUID(path) }

// Replay delivers every event recorded below roots after the event id
// since, in the order of the history, then returns true (also when fn
// stopped it early by returning false). It returns false when FSEvents is
// unavailable, the replay timed out (timeout <= 0 means DefaultTimeout), ctx
// was cancelled or there were too many events to hold. roots must be real
// paths (symlinks resolved and the on-disk case, e.g. /private/var, not
// /var): FSEvents matches them literally. Live events are never delivered.
//
// fseventsd receives events from the kernel with a small delay (tens of
// milliseconds): the history may miss the very last changes. Changes waits
// for them.
func Replay(ctx context.Context, roots []string, since uint64, timeout time.Duration, fn func(Event) bool) bool {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if len(roots) == 0 || since == 0 || !supported {
		return false
	}
	return replay(ctx, roots, since, timeout, fn)
}

// Changes calls fn for every event recorded below roots since the event id
// since, up to the moment of the call included. fseventsd receives events
// from the kernel with a small delay, so Changes writes a marker file in a
// directory of the per-user temporary directory first, and replays the history (never live
// events: fseventsd drops live events for slow or busy clients) until the
// marker is part of it: every change made before the call was recorded
// before the marker. An event may be delivered twice. It returns true when
// every change was delivered, or when fn stopped the replay by returning
// false; false means the history is unavailable (unsupported, timeout, ctx
// cancelled, ids going backwards): trust nothing then.
//
// Events carrying FlagsRescanAll or FlagMustScanSubDirs are delivered like
// the others: the caller must handle them.
func Changes(ctx context.Context, roots []string, since uint64, timeout time.Duration, fn func(Event) bool) bool {
	if !supported || since == 0 || len(roots) == 0 {
		return false
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	deadline := time.Now().Add(timeout)
	before := CurrentEventID()
	if before == 0 || since > before {
		return false
	}
	dir, err := markerDir()
	if err != nil {
		return false
	}
	f, err := os.CreateTemp(dir, "marker-*")
	if err != nil {
		return false
	}
	marker := f.Name()
	f.Close()
	defer os.Remove(marker)

	// The marker directory is busy: it is only watched after the first
	// (possibly long) replay, over a few milliseconds of history.
	extra := !coveredBy(dir, roots)
	stream := roots
	from := since
	pause := 5 * time.Millisecond
	for first := true; ; first = false {
		if extra && !first {
			stream = append(append([]string(nil), roots...), dir)
		}
		rem := time.Until(deadline)
		if rem <= 0 {
			return false
		}
		cur := CurrentEventID() // the history holds every event up to it
		var synced, stopped bool
		ok := Replay(ctx, stream, from, rem, func(e Event) bool {
			switch {
			case e.Path == marker:
				if e.ID > before {
					synced = true
				}
			case extra && coveredBy(e.Path, []string{dir}) && !coveredBy(e.Path, roots) &&
				!(e.Flags&(FlagsRescanAll|FlagMustScanSubDirs) != 0 && above(e.Path, roots)):
				// below the marker directory only (a drop above the roots
				// concerns them)
			default:
				if !fn(e) {
					stopped = true
					return false
				}
			}
			return true
		})
		switch {
		case stopped:
			return ok
		case !ok:
			return false
		case synced:
			return true
		}
		if first && extra {
			// The marker directory was not watched: replay again from
			// before the marker (events of the roots may come twice).
			from = before
			continue
		}
		// The marker is not recorded yet (it would have been delivered if
		// its id were <= cur): replay what came since.
		if cur > from {
			from = cur
		}
		t := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			t.Stop()
			return false
		case <-t.C:
		}
		pause = min(2*pause, 100*time.Millisecond)
	}
}

// markerDir returns a quiet directory for the markers of Changes (a busy one
// would flood the stream): lu-cleaner-fsevents in the per-user temporary
// directory, spelled as FSEvents reports it.
func markerDir() (string, error) {
	tmp, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return "", err
	}
	dir := filepath.Join(tmp, "lu-cleaner-fsevents")
	if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
		return "", err
	}
	return dir, nil
}

// Sync waits until fseventsd has recorded every change made before the call
// and returns the current event id then: a Changes since that id reports
// every change made after Sync was called, and none made before. ok=false
// (unavailable, timeout) returns the current id anyway, which is still safe
// to replay from (changes made just before the call may be reported too).
func Sync(ctx context.Context, timeout time.Duration) (id uint64, ok bool) {
	if !supported {
		return 0, false
	}
	dir, err := markerDir()
	if err != nil {
		return CurrentEventID(), false
	}
	before := CurrentEventID()
	ok = Changes(ctx, []string{dir}, before, timeout, func(Event) bool { return true })
	if !ok {
		return before, false
	}
	return CurrentEventID(), true
}

// ChangedSince returns the distinct paths changed below roots since the
// event id since, up to the moment of the call (see Changes), sorted.
// mustRescanAll is set when the history cannot be trusted (events dropped,
// ids wrapped). Paths flagged FlagMustScanSubDirs are returned too: treat
// every returned directory as changed recursively when in doubt.
func ChangedSince(ctx context.Context, roots []string, since uint64, timeout time.Duration) (paths []string, mustRescanAll bool, ok bool) {
	seen := map[string]bool{}
	ok = Changes(ctx, roots, since, timeout, func(e Event) bool {
		if e.Flags&FlagsRescanAll != 0 {
			mustRescanAll = true
			return false
		}
		if e.Path != "" && !seen[e.Path] {
			seen[e.Path] = true
			paths = append(paths, e.Path)
		}
		return true
	})
	if !ok {
		return nil, false, false
	}
	if mustRescanAll {
		return nil, true, true
	}
	sort.Strings(paths)
	return paths, false, true
}

// above reports whether p is an ancestor of one of roots (a drop or a
// MustScanSubDirs reported there concerns them).
func above(p string, roots []string) bool {
	for _, r := range roots {
		if r != p && coveredBy(r, []string{p}) {
			return true
		}
	}
	return false
}

// coveredBy reports whether p equals one of roots or lies below it.
func coveredBy(p string, roots []string) bool {
	for _, r := range roots {
		if p == r || r == "/" || (len(p) > len(r) && p[:len(r)] == r && p[len(r)] == '/') {
			return true
		}
	}
	return false
}
