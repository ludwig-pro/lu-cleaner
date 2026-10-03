package jsdev

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/diagnostics"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"github.com/ludwig-pro/lu-cleaner/internal/providers/internal/scanmemo"
	"github.com/ludwig-pro/lu-cleaner/internal/scanctl"
	"golang.org/x/sys/unix"
)

const externalWarn = "on external volume — no internal gain"

// scanner holds the state of one Scan call.
type scanner struct {
	ctx  context.Context
	env  *core.Env
	emit core.Emit
	p    *Provider

	homeDev   uint64
	homeDevOK bool

	procs    *procSnapshot
	projects *projectInfo
	shells   []*multishellDir

	// sizes memoizes measurements within the scan (see measure), so the
	// scanners that wait for the project walk can measure beforehand.
	sizesMu sync.Mutex
	sizes   map[sizeKey]*sizeEntry
}

type sizeKey struct {
	path     string
	external bool // measured across devices
}

type sizeEntry struct {
	once scanmemo.Once
	st   fsx.Stats
}

// measure sizes p once per scan (concurrent callers wait for the first
// walk). Without CrossDevice it goes through the run's size cache, like
// before: other providers and later runs may answer it.
func (s *scanner) measure(p string, external bool) fsx.Stats {
	k := sizeKey{p, external}
	s.sizesMu.Lock()
	if s.sizes == nil {
		s.sizes = map[sizeKey]*sizeEntry{}
	}
	e := s.sizes[k]
	if e == nil {
		e = &sizeEntry{}
		s.sizes[k] = e
	}
	s.sizesMu.Unlock()
	if err := e.once.Do(s.ctx, func() {
		e.st, _ = fsx.Size(s.ctx, p, &fsx.Options{CrossDevice: external})
	}); err != nil {
		return fsx.Stats{}
	}
	return e.st
}

// warmWorkers bounds the concurrent walks of one warm call: a single walk
// gets little of the shared walker pool while every scanner walks.
const warmWorkers = 3

// warm measures paths the way sized will (see measure), before the item
// can be built.
func (s *scanner) warm(paths []string) {
	next := make(chan string)
	var wg sync.WaitGroup
	for range min(warmWorkers, len(paths)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer diagnostics.Recover(s.ctx, "js")
			for p := range next {
				s.warmOne(p)
			}
		}()
	}
queue:
	for _, p := range paths {
		if s.ctx.Err() != nil {
			break
		}
		select {
		case next <- p:
		case <-s.ctx.Done():
			break queue
		}
	}
	close(next)
	wg.Wait()
}

func (s *scanner) warmOne(p string) {
	target, external, ok := s.locate(p)
	if !ok {
		return
	}
	if external {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			s.measure(real, true)
		}
		return
	}
	s.measure(target, false)
}

func (s *scanner) logf(format string, args ...any) {
	if s.env.Logf != nil {
		s.env.Logf(format, args...)
	}
}

func (s *scanner) getenv(k string) string {
	if s.p.getenv == nil {
		return ""
	}
	return s.p.getenv(k)
}

// home joins path elements below the home directory.
func (s *scanner) home(rel ...string) string {
	return filepath.Join(append([]string{s.env.Home}, rel...)...)
}

// envDir returns $name when it is an absolute path.
func (s *scanner) envDir(name string, rel ...string) string {
	v := strings.TrimSpace(s.getenv(name))
	if v == "" {
		return ""
	}
	v = s.env.Expand(v)
	if !filepath.IsAbs(v) {
		return ""
	}
	return filepath.Join(append([]string{v}, rel...)...)
}

// uniqDirs keeps existing directories, deduplicated by real path.
func uniqDirs(paths ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range paths {
		if p == "" {
			continue
		}
		p = filepath.Clean(p)
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			continue
		}
		if fi, err := os.Stat(real); err != nil || !fi.IsDir() {
			continue
		}
		if seen[real] {
			continue
		}
		seen[real] = true
		out = append(out, p)
	}
	return out
}

// allowed reports whether p may be proposed at all: it exists and is neither
// excluded by the user nor protected by the safety guard.
func (s *scanner) allowed(p string) bool {
	if p == "" || !filepath.IsAbs(p) || s.env.Excluded(p) || s.env.IsProtectedContext(s.ctx, p) {
		return false
	}
	_, err := fsx.Lstat(s.ctx, p)
	return err == nil
}

// devOf returns the device of the real location of p.
func (s *scanner) devOf(p string) (uint64, bool) {
	if s.p.devOf != nil {
		return s.p.devOf(p)
	}
	var st unix.Stat_t
	if err := scanctl.DoIO(s.ctx, func() error { return unix.Stat(p, &st) }); err != nil {
		return 0, false
	}
	return uint64(st.Dev), true
}

// locate resolves the item path p. It returns the path to delete (the real
// directory when p itself is a symlink on the same volume), whether the real
// location is on another volume (report only), and ok=false for dangling
// symlinks and missing mounts (never proposed).
func (s *scanner) locate(p string) (target string, external, ok bool) {
	fi, err := fsx.Lstat(s.ctx, p)
	if err != nil {
		return "", false, false
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", false, false
	}
	target = p
	if fi.Mode()&os.ModeSymlink != 0 {
		target = real
		if !s.allowed(target) {
			return "", false, false
		}
	}
	if s.homeDevOK {
		dev, ok := s.devOf(real)
		if !ok {
			return "", false, false
		}
		if dev != s.homeDev {
			return p, true, true
		}
	}
	return target, false, true
}

// sized emits it as a placeholder (Sizing=true), measures paths and emits the
// final item (same ID). The first path decides the volume: an item living on
// another volume becomes report-only. useNewest merges the newest mtime of
// the tree into LastUsed (right for caches, wrong for installs). It returns
// the emitted final item (read-only) or nil.
func (s *scanner) sized(it *core.Item, paths []string, useNewest bool) *core.Item {
	if len(paths) == 0 || s.ctx.Err() != nil {
		return nil
	}
	target, external, ok := s.locate(paths[0])
	if !ok {
		return nil
	}
	measure := append([]string(nil), paths...)
	if external {
		real, _ := filepath.EvalSymlinks(paths[0])
		measure = []string{real}
		it.Method = core.MethodReport
		it.Command = nil
		it.Recommended = false
		it.Selectable = false
		it.Warn = externalWarn
		if it.Meta == nil {
			it.Meta = map[string]string{}
		}
		it.Meta["real_path"] = real
	} else if target != paths[0] {
		measure[0] = target
		if it.Path == paths[0] {
			it.Path = target
		} else if len(it.Paths) > 0 && it.Paths[0] == paths[0] {
			it.Paths[0] = target
		}
	}
	it.Sizing = true
	s.emit(it.Clone())

	var bytes, reclaim, files int64
	var newest time.Time
	for _, p := range measure {
		st := s.measure(p, external)
		bytes += st.Bytes
		reclaim += st.Reclaim
		files += st.Files
		if st.Newest.After(newest) {
			newest = st.Newest
		}
	}
	if s.ctx.Err() != nil {
		return nil
	}
	it.Sizing = false
	it.Size, it.Files = bytes, files
	it.SetReclaim(reclaim)
	if useNewest && newest.After(it.LastUsed) && !newest.After(s.env.Now.Add(time.Hour)) {
		it.LastUsed = newest
	}
	s.emit(it)
	return it
}

// base returns a new item of this provider.
func (s *scanner) base(kind, key, name string, risk core.Risk) *core.Item {
	return &core.Item{
		ID:         "js:" + kind + ":" + key,
		Provider:   s.p.ID(),
		Category:   core.CatJS,
		Kind:       kind,
		Name:       name,
		Risk:       risk,
		Method:     core.MethodDelete,
		Selectable: true,
		Meta:       map[string]string{},
	}
}

// newestMtime returns the newest mtime among paths (missing ones ignored).
func newestMtime(paths ...string) time.Time {
	return fsx.NewestOf(paths...)
}

// listDir returns the entries of dir sorted by name (nil when unreadable).
func listDir(ctx context.Context, dir string) []os.DirEntry {
	ents, err := fsx.ReadDir(ctx, dir)
	if err != nil {
		return nil
	}
	return ents
}

// isRealDir reports whether p is a directory (not a symlink to one).
func isRealDir(p string) bool { return fsx.IsDir(p) }

// isDirOrLink reports whether p is a directory or a symlink to one (sized
// then resolves the link to the real directory, or skips it when dangling).
func isDirOrLink(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// globalPackages lists user-installed global npm packages in a lib/node_modules
// directory (npm and corepack, bundled with Node, are ignored).
func globalPackages(ctx context.Context, lib string) []string {
	var out []string
	for _, e := range listDir(ctx, lib) {
		name := e.Name()
		if strings.HasPrefix(name, ".") || name == "npm" || name == "corepack" {
			continue
		}
		if !e.IsDir() && e.Type()&os.ModeSymlink == 0 {
			continue
		}
		if strings.HasPrefix(name, "@") {
			for _, se := range listDir(ctx, filepath.Join(lib, name)) {
				if !strings.HasPrefix(se.Name(), ".") {
					out = append(out, name+"/"+se.Name())
				}
			}
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// joinLimit joins at most n values, with "+k more" when truncated.
func joinLimit(vals []string, n int) string {
	if len(vals) <= n {
		return strings.Join(vals, ", ")
	}
	return strings.Join(vals[:n], ", ") + ", +" + itoa(len(vals)-n) + " more"
}

func itoa(n int) string { return strconv.Itoa(n) }

// volumesDir is where macOS mounts other volumes (a var for tests).
var volumesDir = "/Volumes"

// pathMissing reports whether p surely does not exist: stat fails with
// ENOENT, it is not on an unmounted volume and its nearest existing ancestor
// is a readable directory. Any other error (EACCES, EPERM/TCC, I/O) means
// "unknown", never "missing". So do a dangling symlink on the way (p itself,
// or an ancestor pointing to an unmounted volume) and a /Volumes/X folder
// that is not a mount point (volume unmounted uncleanly).
func pathMissing(p string) bool {
	p = filepath.Clean(p)
	if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
		return false
	}
	// A path on an unmounted volume is unknown too (/Volumes itself is readable).
	if rest, ok := strings.CutPrefix(p, volumesDir+"/"); ok {
		vol, _, _ := strings.Cut(rest, "/")
		if !isMountPoint(filepath.Join(volumesDir, vol)) {
			return false
		}
	}
	for cur := p; ; {
		fi, err := os.Lstat(cur)
		switch {
		case err == nil:
			if cur == p {
				return false // p is a dangling symlink: its target is unknown
			}
			if fi.Mode()&os.ModeSymlink != 0 {
				// The missing part lies behind a symlink: it must resolve
				// (a link to an unmounted volume dangles).
				if fi, err = os.Stat(cur); err != nil {
					return false
				}
			}
			if !fi.IsDir() {
				return false
			}
			f, err := os.Open(cur)
			if err != nil {
				return false
			}
			_, err = f.Readdirnames(1)
			f.Close()
			return err == nil || errors.Is(err, io.EOF)
		case !errors.Is(err, fs.ErrNotExist):
			return false
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return false
		}
		cur = parent
	}
}

// isMountPoint reports whether dir exists and is on another device than its
// parent (a mounted volume, not a leftover folder).
func isMountPoint(dir string) bool {
	var st, pst unix.Stat_t
	if unix.Stat(dir, &st) != nil || unix.Stat(filepath.Dir(dir), &pst) != nil {
		return false
	}
	return st.Dev != pst.Dev
}
