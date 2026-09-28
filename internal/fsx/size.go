// Package fsx contains fast, concurrent filesystem helpers: directory size
// computation (allocated blocks, hardlink aware), human formatting, parsing.
package fsx

import (
	"context"
	"errors"
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
	// Reclaim is what deleting the whole tree would really free: files whose
	// hardlinks live partly outside the tree (pnpm store, ...) are excluded.
	// APFS clones cannot be detected cheaply and are counted as reclaimable.
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
}

type walker struct {
	ctx context.Context
	opt Options
	dev int32

	bytes, apparent, reclaim, files, dirs, errs atomic.Int64
	newest                                      atomic.Int64

	mu    sync.Mutex
	links map[uint64]*hardlink

	wg sync.WaitGroup
}

// ErrNotExist is returned (wrapped) when the root does not exist.
var ErrNotExist = os.ErrNotExist

// Size measures path. Symlinks are never followed (a symlink root counts as
// the link itself). Directories on other devices are skipped unless
// opt.CrossDevice. The walk stops early (returning partial stats and
// ctx.Err()) when ctx is cancelled.
func Size(ctx context.Context, path string, opt *Options) (Stats, error) {
	if c, ok := ctx.Value(cacheKey{}).(*sizeCache); ok && (opt == nil || (opt.Skip == nil && !opt.CrossDevice)) {
		return c.get(ctx, path)
	}
	return size(ctx, path, opt)
}

func size(ctx context.Context, path string, opt *Options) (Stats, error) {
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return Stats{}, &os.PathError{Op: "lstat", Path: path, Err: os.ErrNotExist}
		}
		return Stats{}, &os.PathError{Op: "lstat", Path: path, Err: err}
	}
	w := &walker{ctx: ctx, dev: st.Dev, links: map[uint64]*hardlink{}}
	if opt != nil {
		w.opt = *opt
	}
	w.account(&st)
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
	for _, l := range w.links {
		if l.seen >= l.nlink {
			s.Reclaim += l.size
		}
	}
	w.mu.Unlock()
	return s
}

// account adds one stat record to the totals.
func (w *walker) account(st *unix.Stat_t) {
	alloc := st.Blocks * 512
	w.apparent.Add(st.Size)
	mt := st.Mtim.Nano()
	for {
		cur := w.newest.Load()
		if mt <= cur || w.newest.CompareAndSwap(cur, mt) {
			break
		}
	}
	if st.Mode&unix.S_IFMT == unix.S_IFREG && st.Nlink > 1 {
		w.mu.Lock()
		l, ok := w.links[st.Ino]
		if !ok {
			w.links[st.Ino] = &hardlink{size: alloc, nlink: st.Nlink, seen: 1}
			w.bytes.Add(alloc)
		} else {
			l.seen++
		}
		w.mu.Unlock()
		return
	}
	w.bytes.Add(alloc)
	w.reclaim.Add(alloc)
}

// accountEntry adds one bulk directory entry to the totals.
func (w *walker) accountEntry(alloc, size, mtimeNs int64, regular bool, nlink uint32, ino uint64) {
	w.apparent.Add(size)
	for {
		cur := w.newest.Load()
		if mtimeNs <= cur || w.newest.CompareAndSwap(cur, mtimeNs) {
			break
		}
	}
	if regular && nlink > 1 {
		w.mu.Lock()
		l, ok := w.links[ino]
		if !ok {
			w.links[ino] = &hardlink{size: alloc, nlink: uint16(min(nlink, 65535)), seen: 1}
			w.bytes.Add(alloc)
		} else {
			l.seen++
		}
		w.mu.Unlock()
		return
	}
	w.bytes.Add(alloc)
	w.reclaim.Add(alloc)
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
			w.accountEntry(0, 0, e.mtimeNs, false, 0, 0)
			w.dirs.Add(1)
			subdirs = append(subdirs, child)
		default:
			w.accountEntry(e.alloc, e.size, e.mtimeNs, e.objType == vREG, e.nlink, e.ino)
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
			w.account(&st)
			w.dirs.Add(1)
			subdirs = append(subdirs, child)
			continue
		}
		w.account(&st)
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
	mu sync.Mutex
	m  map[string]*cacheEntry
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
	return context.WithValue(ctx, cacheKey{}, &sizeCache{m: map[string]*cacheEntry{}})
}

func (c *sizeCache) get(ctx context.Context, path string) (Stats, error) {
	c.mu.Lock()
	e, ok := c.m[path]
	if !ok {
		e = &cacheEntry{done: make(chan struct{})}
		c.m[path] = e
		c.mu.Unlock()
		e.st, e.err = size(ctx, path, nil)
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
