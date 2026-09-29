// Package fsx contains fast, concurrent filesystem helpers: directory size
// computation (allocated blocks, hardlink aware), human formatting, parsing.
package fsx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

// Stats is the result of measuring a path.
type Stats struct {
	// Bytes is the allocated size on disk (st_blocks*512). A hardlinked file
	// is counted once per measured tree.
	Bytes int64 `json:"bytes"`
	// Apparent is the sum of logical file sizes.
	Apparent int64 `json:"apparent"`
	// Reclaim is what deleting the whole tree would really free:
	//   - files whose hardlinks live partly outside the tree (pnpm store...)
	//     are excluded;
	//   - APFS clones (bun and pnpm installs, `cp -c`, Finder duplicates)
	//     count only their private bytes, unless every file sharing the same
	//     data is inside the tree (then that data is counted once).
	// It is an estimate: bytes shared between a partly modified clone and its
	// origin both inside the tree may be counted once too many, a partly
	// shared file under 128 KiB counts fully (privateMinAlloc), and data
	// shared between two separately measured trees (the bun cache and a
	// node_modules installed from it) is freed only when both are deleted.
	Reclaim int64     `json:"reclaim"`
	Files   int64     `json:"files"`
	Dirs    int64     `json:"dirs"`
	Newest  time.Time `json:"newest"` // newest mtime of any entry (including root)
	Errors  int64     `json:"errors"` // unreadable entries (permissions...)
}

// Options tunes a Size call. The zero value is fine.
type Options struct {
	// Skip is called for each directory below the root; returning true prunes it
	// (its own size is not counted either).
	Skip func(path, name string) bool
	// CrossDevice allows descending into other mounted filesystems.
	CrossDevice bool
}

// sem bounds the number of goroutines reading directories across ALL
// concurrent Size calls, so scanning many items at once stays well behaved.
var sem = make(chan struct{}, walkers())

func walkers() int {
	if n, err := strconv.Atoi(os.Getenv("LU_WALKERS")); err == nil && n > 0 {
		return n
	}
	return max(8, runtime.NumCPU()*3)
}

type hardlink struct {
	size  int64
	nlink uint16
	seen  uint16
	dev   int32
	share fileShare
}

// volKey identifies an inode or an APFS data stream. Inode numbers and
// clone ids are only unique within one volume, and a CrossDevice walk may
// see several volumes: without the device, two unrelated files would be
// merged (counted once, or their clone families mixed up).
type volKey struct {
	dev int32
	id  uint64
}

// fileShare describes how the data of a regular file is shared with other
// files through APFS clones (getattrlist ATTR_CMNEXT_*).
type fileShare struct {
	known   bool   // the volume reported clone information
	private int64  // bytes held by this inode only (meaningful when refs == 1)
	cloneID uint64 // id of the data stream (ATTR_CMNEXT_CLONEID)
	refs    uint32 // inodes referencing that data stream (0 = no data stream)
}

// cloned reports whether the data stream is shared with other inodes.
func (s fileShare) cloned() bool { return s.known && s.refs > 1 }

// exclusive returns what deleting this inode alone frees.
func (s fileShare) exclusive(alloc int64) int64 {
	if !s.known || s.refs == 0 {
		// No information, or no data stream: a transparently compressed file
		// keeps its data in an extended attribute and reports a private
		// size of 0 although nothing is shared. Count it fully.
		return alloc
	}
	return min(max(s.private, 0), alloc)
}

// cloneFamily gathers the inodes of the tree sharing one data stream.
type cloneFamily struct {
	alloc  int64  // allocated size of the shared data
	refs   uint32 // inodes sharing it, on the whole volume
	inodes uint32 // of which fully inside the tree
	excl   int64  // private bytes of those inodes
}

func (f *cloneFamily) add(alloc int64, s fileShare) {
	f.alloc = max(f.alloc, alloc)
	f.refs = max(f.refs, s.refs)
	f.inodes++
	f.excl += s.exclusive(alloc)
}

// reclaim is what deleting the family's inodes frees: the shared data once
// when no inode outside the tree still references it.
func (f *cloneFamily) reclaim() int64 {
	if f.inodes >= f.refs {
		return max(f.alloc, f.excl)
	}
	return f.excl
}

type walker struct {
	ctx context.Context
	opt Options
	dev int32

	bytes, apparent, reclaim, files, dirs, errs atomic.Int64
	newest                                      atomic.Int64

	mu     sync.Mutex
	links  map[volKey]*hardlink    // regular files with nlink > 1, by inode
	clones map[volKey]*cloneFamily // cloned files with nlink == 1, by data stream id

	wg sync.WaitGroup
}

// ErrNotExist is returned (wrapped) when the root does not exist.
var ErrNotExist = os.ErrNotExist

// Size measures path. Symlinks are never followed (a symlink root counts as
// the link itself). Directories on other devices are skipped unless
// opt.CrossDevice. The walk stops early (returning partial stats and
// ctx.Err()) when ctx is cancelled.
func Size(ctx context.Context, path string, opt *Options) (st Stats, err error) {
	if c, ok := ctx.Value(cacheKey{}).(*sizeCache); ok && (opt == nil || (opt.Skip == nil && !opt.CrossDevice)) {
		return c.get(ctx, path)
	}
	return size(ctx, path, opt)
}

// size walks path (the trace and the counters only see real walks, not
// cache hits).
func size(ctx context.Context, path string, opt *Options) (res Stats, err error) {
	if traceOn {
		start := time.Now()
		defer func() { Trace("walk", path, start, fmt.Sprintf("%d files %s", res.Files, Bytes(res.Bytes))) }()
	}
	defer func() {
		if res.Files+res.Dirs > 0 { // not a missing or refused root
			walkCount.Add(1)
			walkFiles.Add(res.Files)
		}
	}()
	if AppDataProtected(path) {
		// never touch another app's container without Full Disk Access:
		// the access would block on a system permission prompt.
		return Stats{Errors: 1}, &os.PathError{Op: "size", Path: path, Err: ErrNeedsFullDiskAccess}
	}
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return Stats{}, &os.PathError{Op: "lstat", Path: path, Err: os.ErrNotExist}
		}
		return Stats{}, &os.PathError{Op: "lstat", Path: path, Err: err}
	}
	w := &walker{ctx: ctx, dev: st.Dev, links: map[volKey]*hardlink{}, clones: map[volKey]*cloneFamily{}}
	if opt != nil {
		w.opt = *opt
	}
	var sh fileShare
	if st.Mode&unix.S_IFMT == unix.S_IFREG && useBulk && useCloneAttrs {
		sh = shareOf(path)
	}
	w.account(&st, sh)
	if st.Mode&unix.S_IFMT == unix.S_IFDIR {
		w.dirs.Add(1)
		w.walk(path)
		w.wg.Wait()
	} else {
		w.files.Add(1)
	}
	return w.stats(), ctx.Err()
}

func (w *walker) stats() Stats {
	s := Stats{
		Bytes:    w.bytes.Load(),
		Apparent: w.apparent.Load(),
		Reclaim:  w.reclaim.Load(),
		Files:    w.files.Load(),
		Dirs:     w.dirs.Load(),
		Errors:   w.errs.Load(),
	}
	if n := w.newest.Load(); n > 0 {
		s.Newest = time.Unix(0, n)
	}
	w.mu.Lock()
	// Work on copies so stats() can be called more than once.
	families := make(map[volKey]cloneFamily, len(w.clones))
	for id, f := range w.clones {
		families[id] = *f
	}
	for _, l := range w.links {
		if l.seen < l.nlink {
			continue // hardlinked from outside the tree: deleting frees nothing
		}
		if l.share.cloned() {
			k := volKey{l.dev, l.share.cloneID}
			f := families[k]
			f.add(l.size, l.share)
			families[k] = f
			continue
		}
		s.Reclaim += l.share.exclusive(l.size)
	}
	w.mu.Unlock()
	for _, f := range families {
		s.Reclaim += f.reclaim()
	}
	return s
}

// touch records an mtime (nanoseconds) for Stats.Newest.
func (w *walker) touch(mt int64) {
	for {
		cur := w.newest.Load()
		if mt <= cur || w.newest.CompareAndSwap(cur, mt) {
			break
		}
	}
}

// account adds one stat record to the totals.
func (w *walker) account(st *unix.Stat_t, sh fileShare) {
	w.apparent.Add(st.Size)
	w.touch(st.Mtim.Nano())
	if st.Mode&unix.S_IFMT == unix.S_IFDIR {
		w.bytes.Add(st.Blocks * 512)
		w.reclaim.Add(st.Blocks * 512)
		return
	}
	w.accountFile(st.Blocks*512, st.Mode&unix.S_IFMT == unix.S_IFREG, uint32(st.Nlink), st.Dev, st.Ino, sh)
}

// accountFile adds one non-directory entry: hardlinks are counted once, and
// clones and hardlinks shared with files outside the tree are left out of
// the reclaimable bytes (resolved in stats()).
func (w *walker) accountFile(alloc int64, regular bool, nlink uint32, dev int32, ino uint64, sh fileShare) {
	if !regular {
		w.bytes.Add(alloc)
		w.reclaim.Add(alloc)
		return
	}
	if nlink <= 1 && !sh.cloned() {
		w.bytes.Add(alloc)
		w.reclaim.Add(sh.exclusive(alloc))
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if nlink > 1 {
		k := volKey{dev, ino}
		if l, ok := w.links[k]; ok {
			l.seen++
			return
		}
		w.links[k] = &hardlink{size: alloc, nlink: uint16(min(nlink, 65535)), seen: 1, dev: dev, share: sh}
		w.bytes.Add(alloc)
		return
	}
	w.bytes.Add(alloc)
	k := volKey{dev, sh.cloneID}
	f := w.clones[k]
	if f == nil {
		f = &cloneFamily{}
		w.clones[k] = f
	}
	f.add(alloc, sh)
}

// walkBulk lists dir with getattrlistbulk; false means "unsupported here".
func (w *walker) walkBulk(dir string) bool {
	var subdirs []string
	ok, err := readDirBulk(dir, func(e *bulkEntry) {
		if e.hasError || e.name == "" {
			w.errs.Add(1)
			return
		}
		switch e.objType {
		case vDIR:
			if !w.opt.CrossDevice && e.dev != w.dev {
				return
			}
			child := filepath.Join(dir, e.name)
			if w.opt.Skip != nil && w.opt.Skip(child, e.name) {
				return
			}
			w.touch(e.mtimeNs)
			w.dirs.Add(1)
			subdirs = append(subdirs, child)
		default:
			w.apparent.Add(e.size)
			w.touch(e.mtimeNs)
			w.accountFile(e.alloc, e.objType == vREG, e.nlink, e.dev, e.ino, e.share())
			w.files.Add(1)
		}
	})
	if !ok {
		return false
	}
	if err != nil {
		w.errs.Add(1)
		return true
	}
	w.recurse(subdirs)
	return true
}

func (w *walker) recurse(subdirs []string) {
	for _, child := range subdirs {
		select {
		case sem <- struct{}{}:
			w.wg.Add(1)
			go func(p string) {
				defer func() { <-sem; w.wg.Done() }()
				w.walk(p)
			}(child)
		default:
			w.walk(child)
		}
	}
}

func (w *walker) walk(dir string) {
	if w.ctx.Err() != nil {
		return
	}
	if AppDataProtected(dir) {
		w.errs.Add(1) // counted as unreadable, never opened (see appdata.go)
		return
	}
	if useBulk && w.walkBulk(dir) {
		return
	}
	f, err := os.Open(dir)
	if err != nil {
		w.errs.Add(1)
		return
	}
	names, err := f.Readdirnames(-1)
	if err != nil {
		w.errs.Add(1)
	}
	fd := int(f.Fd())
	var subdirs []string
	var st unix.Stat_t
	for _, name := range names {
		if err := unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			w.errs.Add(1)
			continue
		}
		if st.Mode&unix.S_IFMT == unix.S_IFDIR {
			if !w.opt.CrossDevice && st.Dev != w.dev {
				continue
			}
			child := filepath.Join(dir, name)
			if w.opt.Skip != nil && w.opt.Skip(child, name) {
				continue
			}
			w.account(&st, fileShare{})
			w.dirs.Add(1)
			subdirs = append(subdirs, child)
			continue
		}
		w.account(&st, fileShare{})
		w.files.Add(1)
	}
	f.Close()
	w.recurse(subdirs)
}

// useBulk enables getattrlistbulk (macOS); LU_NO_BULK=1 forces lstat.
var useBulk = os.Getenv("LU_NO_BULK") == ""

// Exists reports whether p exists (without following a final symlink).
func Exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// IsDir reports whether p is a real directory (symlinks are not).
func IsDir(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.IsDir()
}

// ModTime returns the mtime of p (zero time if missing).
func ModTime(p string) time.Time {
	fi, err := os.Lstat(p)
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// NewestOf returns the newest mtime among the given paths (missing ones ignored).
func NewestOf(paths ...string) time.Time {
	var t time.Time
	for _, p := range paths {
		if m := ModTime(p); m.After(t) {
			t = m
		}
	}
	return t
}

// Within reports whether p equals parent or is below it.
func Within(p, parent string) bool {
	if p == parent {
		return true
	}
	if parent == "/" {
		return len(p) > 1 && p[0] == '/'
	}
	return len(p) > len(parent) && p[:len(parent)] == parent && p[len(parent)] == '/'
}

// ---------------------------------------------------------------- run cache

type cacheKey struct{}

type sizeCache struct {
	mu    sync.Mutex
	m     map[string]*cacheEntry
	store *SizeStore // persistent cache (nil: none)
}

type cacheEntry struct {
	done chan struct{}
	st   Stats
	err  error
}

// WithCache returns a context whose Size calls (without options) are
// memoized and deduplicated: concurrent requests for the same path wait for
// a single walk. Use one per scan run so providers measuring the same
// directory (a worktree's node_modules…) share the work.
func WithCache(ctx context.Context) context.Context {
	return WithSizeStore(ctx, nil)
}

// WithSizeStore is WithCache backed by a persistent size cache: Size calls
// without options answer from s when the tree did not change since it was
// measured, and complete walks are recorded in s. A nil s is WithCache.
func WithSizeStore(ctx context.Context, s *SizeStore) context.Context {
	return context.WithValue(ctx, cacheKey{}, &sizeCache{m: map[string]*cacheEntry{}, store: s})
}

// measure answers from the persistent cache, or walks and records the walk
// when it is complete.
func (c *sizeCache) measure(ctx context.Context, path string) (Stats, error) {
	s := c.store
	if s == nil || AppDataProtected(path) {
		return size(ctx, path, nil)
	}
	if st, ok := s.lookup(ctx, path); ok {
		return st, nil
	}
	id, idOK := identify(path) // before the walk: a change during it shows next time
	start := time.Now()
	st, err := size(ctx, path, nil)
	if idOK && err == nil && ctx.Err() == nil {
		s.record(path, id, st, time.Since(start))
	}
	return st, err
}

func (c *sizeCache) get(ctx context.Context, path string) (Stats, error) {
	c.mu.Lock()
	e, ok := c.m[path]
	if !ok {
		e = &cacheEntry{done: make(chan struct{})}
		c.m[path] = e
		c.mu.Unlock()
		e.st, e.err = c.measure(ctx, path)
		if ctx.Err() != nil {
			// partial result: do not keep it
			c.mu.Lock()
			delete(c.m, path)
			c.mu.Unlock()
		}
		close(e.done)
		return e.st, e.err
	}
	c.mu.Unlock()
	select {
	case <-e.done:
		return e.st, e.err
	case <-ctx.Done():
		return Stats{}, ctx.Err()
	}
}
