package jsdev

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/diagnostics"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

// fnm creates one <pid>_<epoch-ms> symlink per `fnm env` call (every new
// shell) and never removes them. IMPORTANT: <pid> is the pid of the
// short-lived `fnm env` process itself, NOT of the shell, so "pid not alive"
// says nothing: an open shell (or an app that captured a shell environment,
// like Claude, Cursor, Codex...) still points at its entry through PATH and
// FNM_MULTISHELL_PATH. An entry is stale only when no process can use it:
//   - it was created before the last boot (every process was restarted since), or
//   - it is older than a day, referenced by no running process (command line
//     or initial environment), and no live process started around its creation.
const (
	multishellMinAge      = 24 * time.Hour
	multishellStartBefore = 10 * time.Minute // a shell/app starts at most this long before running `fnm env`
	multishellStartAfter  = 2 * time.Minute
	multishellEntryCost   = 256 // bytes of APFS metadata per symlink (estimate, symlinks have no data blocks)
)

var multishellName = regexp.MustCompile(`^([0-9]+)_([0-9]+)$`)

type multishellEntry struct {
	name    string
	path    string
	pid     int
	created time.Time // from the name (epoch ms); zero when unparsable
	mtime   time.Time // lstat mtime (updated by `fnm use`)
	size    int64
	target  string // raw symlink target
}

type multishellDir struct {
	dir     string
	entries []multishellEntry
}

func (s *scanner) multishellDirs() []string {
	own := s.getenv("FNM_MULTISHELL_PATH")
	var ownParent string
	if own != "" && filepath.IsAbs(own) {
		ownParent = filepath.Dir(filepath.Clean(own))
	}
	return uniqDirs(
		ownParent,
		s.envDir("XDG_RUNTIME_DIR", "fnm_multishells"),
		s.envDir("XDG_STATE_HOME", "fnm_multishells"),
		s.home(".local", "state", "fnm_multishells"),
		s.home("Library", "Caches", "fnm_multishells"),
		filepath.Join(s.env.TmpDir, "fnm_multishells"),
	)
}

// loadMultishells reads every multishell entry (names and symlink targets only).
func (s *scanner) loadMultishells() {
	for _, dir := range s.multishellDirs() {
		if s.ctx.Err() != nil {
			return
		}
		names, err := fsx.ReadNames(s.ctx, dir)
		if err != nil {
			continue
		}
		// One lstat (+ readlink) per entry, in parallel: a folder that
		// collected tens of thousands of entries takes seconds to stat on a
		// cold disk otherwise.
		read := make([]*multishellEntry, len(names))
		var wg sync.WaitGroup
		workers := min(multishellWorkers, len(names))
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer diagnostics.Recover(s.ctx, "js")
				for i := w; i < len(names); i += workers {
					if s.ctx.Err() != nil {
						return
					}
					read[i] = readMultishellEntry(s.ctx, dir, names[i])
				}
			}()
		}
		wg.Wait()
		md := &multishellDir{dir: dir}
		for _, e := range read {
			if e != nil {
				md.entries = append(md.entries, *e)
			}
		}
		s.shells = append(s.shells, md)
	}
}

// multishellWorkers bounds the concurrent lstat calls of loadMultishells.
const multishellWorkers = 8

// readMultishellEntry reads one entry (nil when it is not a multishell entry
// or vanished).
func readMultishellEntry(ctx context.Context, dir, name string) *multishellEntry {
	m := multishellName.FindStringSubmatch(name)
	if m == nil {
		return nil
	}
	p := filepath.Join(dir, name)
	fi, err := fsx.Lstat(ctx, p)
	if err != nil {
		return nil
	}
	e := &multishellEntry{name: name, path: p, mtime: fi.ModTime(), size: fi.Size()}
	e.pid, _ = strconv.Atoi(m[1])
	if ms, err := strconv.ParseInt(m[2], 10, 64); err == nil && ms > 0 {
		e.created = time.UnixMilli(ms)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		e.target, _ = os.Readlink(p)
	}
	return e
}

// fnmLastUse maps an fnm node-versions/<v> directory (real path) to the
// newest multishell symlink pointing at it, directly or through an alias:
// the time a shell last used that version.
func (s *scanner) fnmLastUse() map[string]time.Time {
	resolved := map[string]string{} // raw target -> version dir ("" = unknown)
	out := map[string]time.Time{}
	for _, md := range s.shells {
		for _, e := range md.entries {
			if e.target == "" {
				continue
			}
			vdir, ok := resolved[e.target]
			if !ok {
				t := e.target
				if !filepath.IsAbs(t) {
					t = filepath.Join(md.dir, t)
				}
				if real, err := filepath.EvalSymlinks(t); err == nil {
					vdir = fnmVersionDir(real)
				}
				resolved[e.target] = vdir
			}
			if vdir != "" && e.mtime.After(out[vdir]) {
				out[vdir] = e.mtime
			}
		}
	}
	return out
}

// fnmVersionDir returns ".../node-versions/vX" for a path inside it.
func fnmVersionDir(p string) string {
	const marker = "/node-versions/"
	i := strings.Index(p, marker)
	if i < 0 {
		return ""
	}
	rest := p[i+len(marker):]
	if j := strings.IndexByte(rest, '/'); j >= 0 {
		rest = rest[:j]
	}
	if rest == "" {
		return ""
	}
	return p[:i+len(marker)] + rest
}

// multishellStale decides whether an entry can no longer be used by anyone.
func (s *scanner) multishellStale(e multishellEntry, refs map[string]bool, own string, boot time.Time) bool {
	if e.target == "" || e.name == own || refs[e.name] {
		return false // not a symlink (unknown layout), our own shell, or used by a live process
	}
	if s.p.alive != nil && e.pid > 0 && s.p.alive(e.pid) {
		return false // pid reused or (older fnm) the shell itself: keep, conservative
	}
	created := e.created
	if created.IsZero() {
		created = e.mtime
	}
	if !boot.IsZero() && created.Before(boot) && e.mtime.Before(boot) {
		return true
	}
	last := created
	if e.mtime.After(last) {
		last = e.mtime
	}
	if s.env.Now.Sub(last) < multishellMinAge {
		return false
	}
	return !s.procs.startedAround(created, multishellStartBefore, multishellStartAfter)
}

// multishells emits one group item per fnm_multishells directory with the
// stale entries only.
func (s *scanner) multishells() {
	if !s.procs.ok {
		if len(s.shells) > 0 {
			s.logf("jsdev: process list unavailable, fnm multishells skipped")
		}
		return
	}
	refs := s.procs.multishellRefs()
	own := ""
	if v := s.getenv("FNM_MULTISHELL_PATH"); v != "" {
		own = filepath.Base(filepath.Clean(v))
	}
	var boot time.Time
	if s.p.bootTime != nil {
		boot = s.p.bootTime()
	}
	for _, md := range s.shells {
		if s.ctx.Err() != nil {
			return
		}
		if s.env.Excluded(md.dir) {
			continue
		}
		_, external, ok := s.locate(md.dir)
		if !ok {
			continue
		}
		var paths []string
		var est int64
		var newest time.Time
		kept := 0
		for _, e := range md.entries {
			if !s.multishellStale(e, refs, own, boot) || s.env.IsProtectedContext(s.ctx, e.path) || s.env.Excluded(e.path) {
				kept++
				continue
			}
			paths = append(paths, e.path)
			est += e.size + multishellEntryCost
			if e.mtime.After(newest) {
				newest = e.mtime
			}
		}
		if len(paths) == 0 {
			continue
		}
		it := s.base("fnm-multishells", md.dir, "fnm stale shell links ("+itoa(len(paths))+")", core.RiskSafe)
		it.Paths = paths
		it.AlwaysShow = true // thousands of 0-byte symlinks: count matters, not size
		it.Location = s.env.Pretty(md.dir) + "/…"
		it.Files = int64(len(paths))
		it.Size = est
		it.LastUsed = newest
		it.Recommended = true
		it.Note = "fnm creates a <pid>_<time> symlink per shell and never removes them; these belong to shells and apps that are gone (created before the last boot, or used by no running process). Only the symlinks are removed, never Node itself."
		it.Meta["stale"] = itoa(len(paths))
		it.Meta["kept_in_use"] = itoa(kept)
		it.Meta["size"] = "estimate (symlinks have no data blocks; the gain is inodes and a smaller directory)"
		if !boot.IsZero() {
			it.Meta["boot"] = boot.Format(time.RFC3339)
		}
		if external {
			it.Method = core.MethodReport
			it.Selectable = false
			it.Recommended = false
			it.Warn = externalWarn
		}
		s.emit(it)
	}
}
